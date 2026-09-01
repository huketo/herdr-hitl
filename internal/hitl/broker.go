package hitl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// settleTimeout bounds the "clean up the chat message" work that runs after a
// request resolves. It is deliberately independent of the caller's context:
// the caller may already be gone (Ctrl-C) and we still want the buttons off.
const settleTimeout = 20 * time.Second

// NewID returns a short, unique request id. It stays under 16 characters so
// that `hitl:<id>:c:<n>` fits both the Telegram 64-byte callback_data budget
// and the Discord 100-character custom_id budget.
func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on supported platforms; falling back to
		// a timestamp keeps the daemon alive rather than panicking a request.
		return fmt.Sprintf("%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	return hex.EncodeToString(b[:])
}

// Poster is a messenger backend. Implementations live in
// internal/transport/*; the broker only sees this interface.
//
// Post must not block on human input: it delivers the question and returns.
// Answers come back asynchronously through Broker.Resolve.
type Poster interface {
	// Name is the stable transport id used by --transport and config
	// ("telegram", "discord").
	Name() string
	// Post delivers req to the human. It returns an error only when delivery
	// failed; a successful Post means the question is visible.
	Post(ctx context.Context, req *Request) error
	// Settle finalises the delivered message: disable buttons and show the
	// outcome. It runs exactly once per successful Post.
	Settle(ctx context.Context, req *Request, ans *Answer) error
}

type entry struct {
	req    *Request
	posted []Poster
	done   chan *Answer
	once   sync.Once
	// resolved flips as soon as an answer wins the race. The entry stays in
	// the pending map until Settle has finished, so a second click on a
	// still-visible button is reported as "already answered" rather than as
	// an unknown request.
	resolved atomic.Bool
}

func (e *entry) complete(ans *Answer) bool {
	delivered := false
	e.once.Do(func() {
		e.resolved.Store(true)
		e.done <- ans
		close(e.done)
		delivered = true
	})
	return delivered
}

// Broker pairs outstanding questions with incoming answers. It is safe for
// concurrent use: one daemon serves every agent on the machine.
type Broker struct {
	// Now is injectable for tests. Defaults to time.Now.
	Now func() time.Time

	log *slog.Logger

	mu      sync.Mutex
	posters []Poster
	pending map[string]*entry
	// recent remembers the ids of requests that already finished. A human
	// whose click lands just after the message was settled deserves "already
	// answered", not "unknown request"; the ring bounds the memory this
	// courtesy costs.
	recent     map[string]struct{}
	recentRing []string
	recentNext int
}

// recentCapacity is how many finished request ids the broker remembers. A few
// hundred covers every plausible late click; the daemon can run for weeks.
const recentCapacity = 512

// NewBroker returns a broker with no transports registered.
func NewBroker(log *slog.Logger) *Broker {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Broker{
		Now:        time.Now,
		log:        log,
		pending:    make(map[string]*entry),
		recent:     make(map[string]struct{}, recentCapacity),
		recentRing: make([]string, recentCapacity),
	}
}

// retire moves a finished request out of the pending map and into the recent
// ring. The caller must hold b.mu.
func (b *Broker) retire(id string) {
	delete(b.pending, id)
	if _, dup := b.recent[id]; dup {
		return
	}
	if evicted := b.recentRing[b.recentNext]; evicted != "" {
		delete(b.recent, evicted)
	}
	b.recentRing[b.recentNext] = id
	b.recentNext = (b.recentNext + 1) % len(b.recentRing)
	b.recent[id] = struct{}{}
}

// Register adds a transport. Registering the same name twice replaces the
// earlier one, which keeps config reloads simple.
func (b *Broker) Register(p Poster) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, existing := range b.posters {
		if existing.Name() == p.Name() {
			b.posters[i] = p
			return
		}
	}
	b.posters = append(b.posters, p)
}

// TransportNames lists registered transports in registration order.
func (b *Broker) TransportNames() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, len(b.posters))
	for i, p := range b.posters {
		names[i] = p.Name()
	}
	return names
}

// selectPosters resolves the requested transport names. An empty list means
// every registered transport.
func (b *Broker) selectPosters(names []string) ([]Poster, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.posters) == 0 {
		return nil, fmt.Errorf("%w: no transport is configured", ErrNoTransport)
	}
	if len(names) == 0 {
		out := make([]Poster, len(b.posters))
		copy(out, b.posters)
		return out, nil
	}

	byName := make(map[string]Poster, len(b.posters))
	available := make([]string, 0, len(b.posters))
	for _, p := range b.posters {
		byName[p.Name()] = p
		available = append(available, p.Name())
	}

	out := make([]Poster, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || name == "all" {
			continue
		}
		p, ok := byName[name]
		if !ok {
			sort.Strings(available)
			return nil, fmt.Errorf("%w: transport %q is not enabled (available: %s)",
				ErrNoTransport, name, strings.Join(available, ", "))
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		out = make([]Poster, len(b.posters))
		copy(out, b.posters)
	}
	return out, nil
}

// prepare validates the request and fills in server-side fields.
func (b *Broker) prepare(req *Request) error {
	if req == nil {
		return errors.New("hitl: nil request")
	}
	if req.ID == "" {
		req.ID = NewID()
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = b.Now()
	}
	return req.Validate()
}

// fanout posts to every selected transport and reports which ones accepted it.
// A partial failure is tolerated: as long as one human can see the question,
// the ask proceeds.
func (b *Broker) fanout(ctx context.Context, req *Request, posters []Poster) ([]Poster, error) {
	type result struct {
		p   Poster
		err error
	}
	results := make([]result, len(posters))
	var wg sync.WaitGroup
	for i, p := range posters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = result{p: p, err: p.Post(ctx, req)}
		}()
	}
	wg.Wait()

	posted := make([]Poster, 0, len(posters))
	var errs []error
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.p.Name(), r.err))
			continue
		}
		posted = append(posted, r.p)
	}
	if len(posted) == 0 {
		return nil, fmt.Errorf("%w: every transport failed: %w", ErrNoTransport, errors.Join(errs...))
	}
	for _, err := range errs {
		b.log.Warn("transport delivery failed", "request_id", req.ID, "error", err)
	}
	return posted, nil
}

// Submit delivers req and blocks until a human answers, the timeout elapses,
// the request is canceled, or ctx is done. The returned Answer is always
// non-nil when err is nil, and carries a non-answered Status for timeouts and
// cancellations, so callers can report the outcome instead of guessing.
func (b *Broker) Submit(ctx context.Context, req *Request) (*Answer, error) {
	if err := b.prepare(req); err != nil {
		return nil, err
	}
	posters, err := b.selectPosters(req.Transports)
	if err != nil {
		return nil, err
	}

	e := &entry{req: req, done: make(chan *Answer, 1)}
	b.mu.Lock()
	b.pending[req.ID] = e
	b.mu.Unlock()

	posted, err := b.fanout(ctx, req, posters)
	if err != nil {
		b.mu.Lock()
		delete(b.pending, req.ID)
		b.mu.Unlock()
		return nil, err
	}
	e.posted = posted
	b.log.Info("question delivered",
		"request_id", req.ID,
		"transports", posterNames(posted),
		"choices", len(req.Choices),
		"attachments", len(req.Attachments))

	ans := b.wait(ctx, e)

	// Take the controls down before forgetting the request: while Settle is
	// still running the buttons are live, and a click in that window must be
	// told the question is already answered.
	b.settle(req, e.posted, ans)

	b.mu.Lock()
	b.retire(req.ID)
	b.mu.Unlock()

	return ans, nil
}

// wait blocks for the first of: an answer, the deadline, or caller cancellation.
func (b *Broker) wait(ctx context.Context, e *entry) *Answer {
	var timeout <-chan time.Time
	if e.req.Timeout > 0 {
		timer := time.NewTimer(e.req.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case ans := <-e.done:
		return ans
	case <-timeout:
		ans := &Answer{
			RequestID:  e.req.ID,
			Status:     StatusTimeout,
			AnsweredAt: b.Now(),
			Reason:     fmt.Sprintf("no answer within %s", e.req.Timeout),
		}
		e.complete(ans)
		return <-e.done
	case <-ctx.Done():
		// The human reads this in a chat message, so it must not leak a Go
		// error string. A canceled context here means the CLI hung up, which
		// in practice means the agent was interrupted or its pane went away.
		reason := "the agent stopped waiting"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "the agent's own deadline passed"
		}
		ans := &Answer{
			RequestID:  e.req.ID,
			Status:     StatusCanceled,
			AnsweredAt: b.Now(),
			Reason:     reason,
		}
		e.complete(ans)
		return <-e.done
	}
}

// settle strips the interactive controls from every message we posted. It runs
// synchronously with its own deadline so the answer is never printed while a
// live button is still clickable.
func (b *Broker) settle(req *Request, posted []Poster, ans *Answer) {
	if len(posted) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), settleTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, p := range posted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Settle(ctx, req, ans); err != nil {
				b.log.Warn("settle failed", "request_id", req.ID, "transport", p.Name(), "error", err)
			}
		}()
	}
	wg.Wait()
}

// Notify delivers a message that expects no answer. Any choices are dropped:
// nothing would be listening for the click.
func (b *Broker) Notify(ctx context.Context, req *Request) error {
	req.Notice = true
	req.Choices = nil
	req.AllowFreeText = false
	if err := b.prepare(req); err != nil {
		return err
	}
	posters, err := b.selectPosters(req.Transports)
	if err != nil {
		return err
	}
	_, err = b.fanout(ctx, req, posters)
	return err
}

// Resolve records a human's answer. Transports call it from their receive
// loop. The second and later answers for one request lose the race and get
// ErrAlreadyAnswered, which transports surface as "already answered".
func (b *Broker) Resolve(ans *Answer) error {
	if ans == nil || ans.RequestID == "" {
		return ErrUnknownRequest
	}
	b.mu.Lock()
	e, ok := b.pending[ans.RequestID]
	if !ok {
		_, late := b.recent[ans.RequestID]
		b.mu.Unlock()
		if late {
			return ErrAlreadyAnswered
		}
		return ErrUnknownRequest
	}
	b.mu.Unlock()

	if ans.Status == "" {
		ans.Status = StatusAnswered
	}
	if ans.AnsweredAt.IsZero() {
		ans.AnsweredAt = b.Now()
	}
	if ans.ChoiceID != "" {
		if c, found := e.req.ChoiceByID(ans.ChoiceID); found {
			ans.ChoiceLabel = c.Label
			if ans.Text == "" {
				ans.Text = c.Label
			}
		}
	}
	if ans.Status == StatusAnswered && strings.TrimSpace(ans.Text) == "" && ans.ChoiceID == "" {
		return errors.New("hitl: answer carries neither a choice nor text")
	}

	if !e.complete(ans) {
		return ErrAlreadyAnswered
	}
	b.log.Info("answer received",
		"request_id", ans.RequestID,
		"transport", ans.Responder.Transport,
		"choice_id", ans.ChoiceID)
	return nil
}

// Cancel withdraws a pending request.
func (b *Broker) Cancel(id, reason string) error {
	b.mu.Lock()
	e, ok := b.pending[id]
	b.mu.Unlock()
	if !ok {
		return ErrUnknownRequest
	}
	if reason == "" {
		reason = "canceled"
	}
	if !e.complete(&Answer{
		RequestID:  id,
		Status:     StatusCanceled,
		AnsweredAt: b.Now(),
		Reason:     reason,
	}) {
		return ErrAlreadyAnswered
	}
	return nil
}

// Pending snapshots the requests still awaiting a human, oldest first. A
// request that has been answered but is still being settled is excluded.
func (b *Broker) Pending() []*Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Request, 0, len(b.pending))
	for _, e := range b.pending {
		if e.resolved.Load() {
			continue
		}
		out = append(out, e.req)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Lookup returns a pending request by id.
func (b *Broker) Lookup(id string) (*Request, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.pending[id]
	if !ok {
		return nil, false
	}
	return e.req, true
}

// PendingCount reports how many requests are awaiting a human.
func (b *Broker) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, e := range b.pending {
		if !e.resolved.Load() {
			n++
		}
	}
	return n
}

func posterNames(posters []Poster) []string {
	names := make([]string, len(posters))
	for i, p := range posters {
		names[i] = p.Name()
	}
	return names
}
