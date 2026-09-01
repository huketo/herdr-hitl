package hitl_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// fakePoster records what the broker asked it to do and lets a test drive the
// answer side by hand.
type fakePoster struct {
	name string

	mu       sync.Mutex
	posted   []*hitl.Request
	settled  []*hitl.Answer
	postErr  error
	onPost   func(*hitl.Request)
	settleCh chan struct{}
}

func newFakePoster(name string) *fakePoster {
	return &fakePoster{name: name, settleCh: make(chan struct{}, 8)}
}

func (f *fakePoster) Name() string { return f.name }

func (f *fakePoster) Post(_ context.Context, req *hitl.Request) error {
	f.mu.Lock()
	if f.postErr != nil {
		err := f.postErr
		f.mu.Unlock()
		return err
	}
	f.posted = append(f.posted, req)
	onPost := f.onPost
	f.mu.Unlock()

	if onPost != nil {
		onPost(req)
	}
	return nil
}

func (f *fakePoster) Settle(_ context.Context, _ *hitl.Request, ans *hitl.Answer) error {
	f.mu.Lock()
	f.settled = append(f.settled, ans)
	f.mu.Unlock()
	select {
	case f.settleCh <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakePoster) postedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posted)
}

func (f *fakePoster) settledAnswers() []*hitl.Answer {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*hitl.Answer, len(f.settled))
	copy(out, f.settled)
	return out
}

func approvalRequest() *hitl.Request {
	return &hitl.Request{
		Title: "Deploy?",
		Body:  "Ship main to production?",
		Choices: []hitl.Choice{
			{ID: "approve", Label: "Approve", Style: hitl.StylePrimary},
			{ID: "reject", Label: "Reject", Style: hitl.StyleDanger},
		},
		AllowFreeText: true,
	}
}

func TestSubmitReturnsTheChoiceAHumanPicked(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	poster.onPost = func(req *hitl.Request) {
		// A real transport resolves from its receive loop; do the same here.
		go func() {
			_ = broker.Resolve(&hitl.Answer{
				RequestID: req.ID,
				ChoiceID:  "approve",
				Responder: hitl.Responder{Transport: "fake", Username: "huke"},
			})
		}()
	}
	broker.Register(poster)

	ans, err := broker.Submit(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !ans.Answered() {
		t.Fatalf("status = %q, want answered", ans.Status)
	}
	if ans.ChoiceID != "approve" {
		t.Errorf("ChoiceID = %q, want approve", ans.ChoiceID)
	}
	// Resolve fills the label and the plain-text form from the choice table so
	// that `ask -o text` prints something meaningful for a button press.
	if ans.ChoiceLabel != "Approve" || ans.Text != "Approve" {
		t.Errorf("label/text = %q/%q, want Approve/Approve", ans.ChoiceLabel, ans.Text)
	}
	if got := poster.settledAnswers(); len(got) != 1 {
		t.Fatalf("settled %d times, want 1", len(got))
	}
	if broker.PendingCount() != 0 {
		t.Errorf("pending = %d after resolution, want 0", broker.PendingCount())
	}
}

func TestSubmitAcceptsFreeText(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	poster.onPost = func(req *hitl.Request) {
		go func() {
			_ = broker.Resolve(&hitl.Answer{
				RequestID: req.ID,
				Text:      "use pgbouncer, pool size 20",
				Responder: hitl.Responder{Transport: "fake"},
			})
		}()
	}
	broker.Register(poster)

	req := &hitl.Request{Body: "Which connection pooler?", AllowFreeText: true}
	ans, err := broker.Submit(t.Context(), req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ans.Text != "use pgbouncer, pool size 20" {
		t.Errorf("Text = %q", ans.Text)
	}
	if ans.ChoiceID != "" {
		t.Errorf("ChoiceID = %q, want empty for a free-text answer", ans.ChoiceID)
	}
}

func TestSubmitTimesOutAndStillSettles(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	broker.Register(poster)

	req := approvalRequest()
	req.Timeout = 20 * time.Millisecond

	ans, err := broker.Submit(t.Context(), req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ans.Status != hitl.StatusTimeout {
		t.Fatalf("status = %q, want timeout", ans.Status)
	}
	if !errors.Is(ans.Err(), hitl.ErrTimeout) {
		t.Errorf("Err() = %v, want ErrTimeout", ans.Err())
	}
	// The buttons must come down even when nobody pressed one, otherwise the
	// chat keeps a live control for a request that no longer exists.
	settled := poster.settledAnswers()
	if len(settled) != 1 || settled[0].Status != hitl.StatusTimeout {
		t.Fatalf("settled = %+v, want one timeout settle", settled)
	}
}

func TestSubmitCancelsWhenTheCallerGoesAway(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	broker.Register(poster)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	ans, err := broker.Submit(ctx, approvalRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ans.Status != hitl.StatusCanceled {
		t.Fatalf("status = %q, want canceled", ans.Status)
	}
	if settled := poster.settledAnswers(); len(settled) != 1 {
		t.Fatalf("settled %d times, want 1", len(settled))
	}
}

func TestResolveIsRaceFreeAcrossTransports(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	first := newFakePoster("first")
	second := newFakePoster("second")
	broker.Register(first)
	broker.Register(second)

	var (
		ready = make(chan string, 1)
		once  sync.Once
	)
	first.onPost = func(req *hitl.Request) { once.Do(func() { ready <- req.ID }) }
	second.onPost = func(req *hitl.Request) { once.Do(func() { ready <- req.ID }) }

	done := make(chan *hitl.Answer, 1)
	go func() {
		ans, err := broker.Submit(t.Context(), approvalRequest())
		if err != nil {
			t.Errorf("Submit: %v", err)
		}
		done <- ans
	}()

	id := <-ready

	// Two humans hit the button at once, one on each messenger. Exactly one
	// answer may win; the loser must be told so its transport can say so.
	var (
		wg      sync.WaitGroup
		results = make([]error, 2)
	)
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = broker.Resolve(&hitl.Answer{
				RequestID: id,
				ChoiceID:  "approve",
				Responder: hitl.Responder{Transport: "fake"},
			})
		}()
	}
	close(start)
	wg.Wait()

	var won, lost int
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, hitl.ErrAlreadyAnswered):
			lost++
		default:
			t.Fatalf("unexpected Resolve error: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("won=%d lost=%d, want 1/1", won, lost)
	}
	if ans := <-done; !ans.Answered() {
		t.Fatalf("status = %q, want answered", ans.Status)
	}
	if first.postedCount() != 1 || second.postedCount() != 1 {
		t.Errorf("fan-out posted %d/%d, want 1/1", first.postedCount(), second.postedCount())
	}
}

func TestSubmitSurvivesOnePartiallyBrokenTransport(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	broken := newFakePoster("broken")
	broken.postErr = errors.New("network down")
	working := newFakePoster("working")
	working.onPost = func(req *hitl.Request) {
		go func() { _ = broker.Resolve(&hitl.Answer{RequestID: req.ID, ChoiceID: "approve"}) }()
	}
	broker.Register(broken)
	broker.Register(working)

	ans, err := broker.Submit(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !ans.Answered() {
		t.Fatalf("status = %q, want answered", ans.Status)
	}
	// A transport that never delivered must not be asked to settle.
	if got := broken.settledAnswers(); len(got) != 0 {
		t.Errorf("broken transport settled %d times, want 0", len(got))
	}
}

func TestSubmitFailsWhenEveryTransportFails(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	broken := newFakePoster("broken")
	broken.postErr = errors.New("network down")
	broker.Register(broken)

	_, err := broker.Submit(t.Context(), approvalRequest())
	if !errors.Is(err, hitl.ErrNoTransport) {
		t.Fatalf("err = %v, want ErrNoTransport", err)
	}
	if broker.PendingCount() != 0 {
		t.Errorf("pending = %d after a failed post, want 0", broker.PendingCount())
	}
}

func TestSubmitRejectsUnknownTransport(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	broker.Register(newFakePoster("telegram"))

	req := approvalRequest()
	req.Transports = []string{"discord"}

	_, err := broker.Submit(t.Context(), req)
	if !errors.Is(err, hitl.ErrNoTransport) {
		t.Fatalf("err = %v, want ErrNoTransport", err)
	}
}

func TestResolveUnknownRequest(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	if err := broker.Resolve(&hitl.Answer{RequestID: "nope", Text: "hi"}); !errors.Is(err, hitl.ErrUnknownRequest) {
		t.Fatalf("err = %v, want ErrUnknownRequest", err)
	}
}

func TestCancelUnblocksAPendingRequest(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	poster.onPost = func(req *hitl.Request) {
		go func() { _ = broker.Cancel(req.ID, "operator canceled") }()
	}
	broker.Register(poster)

	ans, err := broker.Submit(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ans.Status != hitl.StatusCanceled || ans.Reason != "operator canceled" {
		t.Fatalf("answer = %+v, want canceled with a reason", ans)
	}
}

func TestPendingListsOutstandingRequestsOldestFirst(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	posted := make(chan *hitl.Request, 2)
	poster.onPost = func(req *hitl.Request) { posted <- req }
	broker.Register(poster)

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var tick atomic.Int64
	broker.Now = func() time.Time {
		return base.Add(time.Duration(tick.Add(1)) * time.Second)
	}

	for range 2 {
		go func() { _, _ = broker.Submit(t.Context(), approvalRequest()) }()
	}
	first := <-posted
	second := <-posted

	pending := broker.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	if pending[0].CreatedAt.After(pending[1].CreatedAt) {
		t.Errorf("pending is not oldest-first: %v then %v", pending[0].CreatedAt, pending[1].CreatedAt)
	}
	for _, req := range []*hitl.Request{first, second} {
		if _, ok := broker.Lookup(req.ID); !ok {
			t.Errorf("Lookup(%q) missed a pending request", req.ID)
		}
		_ = broker.Cancel(req.ID, "test teardown")
	}
}

func TestNotifyNeedsNoControls(t *testing.T) {
	t.Parallel()

	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	broker.Register(poster)

	err := broker.Notify(t.Context(), &hitl.Request{
		Title:   "Run finished",
		Body:    "12 tests passed",
		Choices: []hitl.Choice{{ID: "ok", Label: "OK"}},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if broker.PendingCount() != 0 {
		t.Errorf("a notification must not become pending, got %d", broker.PendingCount())
	}
	posted := poster.posted
	if len(posted) != 1 {
		t.Fatalf("posted %d messages, want 1", len(posted))
	}
	if len(posted[0].Choices) != 0 {
		t.Errorf("notification kept %d choices, want none", len(posted[0].Choices))
	}
}

func TestNewIDIsShortAndUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := hitl.NewID()
		if len(id) > 16 {
			t.Fatalf("id %q is %d chars; callback_data budgets need <= 16", id, len(id))
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestLateAnswerIsReportedAsAlreadyAnswered(t *testing.T) {
	t.Parallel()

	// A human can press a button in the moment between the answer landing and
	// the message being edited. Telling them "unknown request" would be a lie;
	// the broker remembers recently finished ids for exactly this case.
	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	poster.onPost = func(req *hitl.Request) {
		go func() { _ = broker.Resolve(&hitl.Answer{RequestID: req.ID, ChoiceID: "approve"}) }()
	}
	broker.Register(poster)

	req := approvalRequest()
	if _, err := broker.Submit(t.Context(), req); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	err := broker.Resolve(&hitl.Answer{RequestID: req.ID, ChoiceID: "reject"})
	if !errors.Is(err, hitl.ErrAlreadyAnswered) {
		t.Fatalf("late Resolve = %v, want ErrAlreadyAnswered", err)
	}
	if err := broker.Resolve(&hitl.Answer{RequestID: "never-existed", Text: "x"}); !errors.Is(err, hitl.ErrUnknownRequest) {
		t.Fatalf("Resolve of an unknown id = %v, want ErrUnknownRequest", err)
	}
}

func TestNoticeCarriesNoControls(t *testing.T) {
	t.Parallel()

	// A fire-and-forget message must not invite an answer: nothing is
	// listening, so a button or a reply prompt would be a dead end.
	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	broker.Register(poster)

	err := broker.Notify(t.Context(), &hitl.Request{
		Title:         "Run finished",
		Body:          "12 tests passed",
		Choices:       []hitl.Choice{{ID: "ok", Label: "OK"}},
		AllowFreeText: true,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	posted := poster.posted[0]
	if !posted.Notice || posted.WantsAnswer() {
		t.Errorf("notice flag = %v, WantsAnswer = %v; want true/false", posted.Notice, posted.WantsAnswer())
	}
	if len(posted.Choices) != 0 || posted.AllowFreeText {
		t.Errorf("notice kept controls: choices=%d free=%v", len(posted.Choices), posted.AllowFreeText)
	}
}

func TestCancellationReasonIsHumanReadable(t *testing.T) {
	t.Parallel()

	// The reason is rendered into a chat message, so a raw Go error string
	// would leak "context canceled" to the human.
	broker := hitl.NewBroker(nil)
	poster := newFakePoster("fake")
	broker.Register(poster)

	ctx, cancel := context.WithCancel(t.Context())
	poster.onPost = func(*hitl.Request) { cancel() }

	ans, err := broker.Submit(ctx, approvalRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ans.Status != hitl.StatusCanceled {
		t.Fatalf("status = %q, want canceled", ans.Status)
	}
	if strings.Contains(ans.Reason, "context") {
		t.Errorf("reason = %q, want prose rather than a Go error", ans.Reason)
	}
	if ans.Reason == "" {
		t.Error("a cancellation must explain itself")
	}
}
