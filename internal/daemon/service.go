package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/herdrctl"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

// paneTokenName is the metadata token published on the asking pane. Herdr
// renders it in the pane's sidebar row, so a human scanning the session sees
// which pane is blocked.
const paneTokenName = "hitl"

// cliTransport labels answers that came from `herdr-hitl answer` instead of a
// messenger.
const cliTransport = "cli"

// herdrNotifier is the slice of the Herdr CLI the daemon uses. Depending on an
// interface rather than *herdrctl.Client keeps the callbacks testable: the
// concrete client shells out, and a test suite must not need Herdr installed.
type herdrNotifier interface {
	Available() bool
	Notify(ctx context.Context, title, body, sound string) error
	SetPaneToken(ctx context.Context, paneID, name, value string, ttl time.Duration) error
	ClearPaneToken(ctx context.Context, paneID, name string) error
}

// service implements ipc.Handler on top of the broker. It owns the
// daemon-local concerns the broker deliberately knows nothing about: config
// defaults, attachment resolution, Herdr callbacks, and liveness accounting.
type service struct {
	cfg      *config.Config
	broker   *hitl.Broker
	herdr    herdrNotifier
	log      *slog.Logger
	endpoint string
	version  string
	start    time.Time
	now      func() time.Time

	// describe reports each live transport's summary. Run installs it once
	// the transports are up; nil means none were registered.
	describe func() []string

	// stop is closed once by Shutdown. Run watches it rather than tearing the
	// process down inline, so the response to `shutdown` is written before
	// the listener closes.
	stop     chan struct{}
	stopOnce sync.Once

	// herdrWG tracks the best-effort Herdr callbacks so a shutdown does not
	// leave a stale `$hitl` token behind on a pane.
	herdrWG sync.WaitGroup

	mu       sync.Mutex
	inflight int
	idleFrom time.Time
}

// newService wires a handler around an already-running broker.
func newService(cfg *config.Config, broker *hitl.Broker, log *slog.Logger, endpoint, version string) *service {
	now := time.Now
	return &service{
		cfg:      cfg,
		broker:   broker,
		herdr:    herdrctl.New(log),
		log:      log,
		endpoint: endpoint,
		version:  version,
		start:    now(),
		now:      now,
		stop:     make(chan struct{}),
		idleFrom: now(),
	}
}

// Ask posts a question and blocks until a human answers, the deadline passes,
// or the calling CLI hangs up.
func (s *service) Ask(ctx context.Context, p *ipc.AskParams) (*hitl.Answer, error) {
	defer s.enter()()

	req, err := s.request(p)
	if err != nil {
		return nil, err
	}

	// waiting is closed when the ask returns, whatever the outcome; the pane
	// token tracker uses it as its "clear now" signal.
	waiting := make(chan struct{})
	defer close(waiting)
	s.trackPane(req, waiting)
	s.toast(req.Title, askSummary(req), herdrctl.SoundRequest)

	ans, err := s.broker.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	s.toast(req.Title, answerSummary(ans), herdrctl.SoundDone)
	return ans, nil
}

// Notify posts a message nobody has to answer.
func (s *service) Notify(ctx context.Context, p *ipc.AskParams) error {
	defer s.enter()()

	req, err := s.request(p)
	if err != nil {
		return err
	}
	return s.broker.Notify(ctx, req)
}

// Pending lists the outstanding questions.
func (s *service) Pending(context.Context) ([]*hitl.Request, error) {
	defer s.enter()()
	return s.broker.Pending(), nil
}

// AnswerRequest resolves a question from the terminal, which is the escape
// hatch when the messenger is unreachable.
func (s *service) AnswerRequest(_ context.Context, p *ipc.AnswerParams) error {
	defer s.enter()()

	if p == nil || strings.TrimSpace(p.RequestID) == "" {
		return fmt.Errorf("%w: no request id", hitl.ErrUnknownRequest)
	}
	responder := strings.TrimSpace(p.Responder)
	if responder == "" {
		responder = localUser()
	}
	return s.broker.Resolve(&hitl.Answer{
		RequestID:  p.RequestID,
		Status:     hitl.StatusAnswered,
		ChoiceID:   p.ChoiceID,
		Text:       p.Text,
		Responder:  hitl.Responder{Transport: cliTransport, Username: responder},
		AnsweredAt: s.now(),
	})
}

// CancelRequest withdraws a question.
func (s *service) CancelRequest(_ context.Context, p *ipc.CancelParams) error {
	defer s.enter()()

	if p == nil || strings.TrimSpace(p.RequestID) == "" {
		return fmt.Errorf("%w: no request id", hitl.ErrUnknownRequest)
	}
	return s.broker.Cancel(p.RequestID, p.Reason)
}

// transportDescriptions returns the live transport summaries, or nil.
func (s *service) transportDescriptions() []string {
	if s.describe == nil {
		return nil
	}
	return s.describe()
}

// Status reports what the daemon is doing.
func (s *service) Status(context.Context) (*ipc.Status, error) {
	defer s.enter()()

	now := s.now()
	return &ipc.Status{
		PID:          os.Getpid(),
		Version:      s.version,
		Socket:       s.endpoint,
		Transports:   s.broker.TransportNames(),
		Descriptions: s.transportDescriptions(),
		Pending:      s.broker.PendingCount(),
		StartedAt:    s.start.Format(time.RFC3339),
		Uptime:       now.Sub(s.start).Truncate(time.Second).String(),
	}, nil
}

// Shutdown asks Run to stop. It returns immediately so the server can write
// the response; the process winds down once that connection is done.
func (s *service) Shutdown(context.Context) error {
	s.requestStop()
	return nil
}

// requestStop closes the stop channel at most once.
func (s *service) requestStop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// request maps wire parameters onto a domain request, filling in the config
// defaults and resolving attachment paths on this machine.
func (s *service) request(p *ipc.AskParams) (*hitl.Request, error) {
	if p == nil {
		return nil, errors.New("daemon: missing ask parameters")
	}
	req := &hitl.Request{
		ID:            hitl.NewID(),
		Title:         p.Title,
		Body:          p.Body,
		Choices:       p.Choices,
		AllowFreeText: p.AllowFreeText,
		Timeout:       time.Duration(p.Timeout),
		Transports:    p.Transports,
		Origin:        p.Origin,
		CreatedAt:     s.now(),
	}
	if req.Timeout == 0 {
		req.Timeout = s.cfg.Timeout.Duration()
	}
	if len(req.Transports) == 0 {
		req.Transports = s.cfg.DefaultTransports()
	}
	for _, path := range p.Attachments {
		att, err := hitl.NewAttachment(path)
		if err != nil {
			return nil, fmt.Errorf("daemon: attachment %s: %w", path, err)
		}
		req.Attachments = append(req.Attachments, att)
	}
	return req, nil
}

// trackPane publishes the `$hitl` pane token for the lifetime of one ask. The
// set and the clear share a goroutine so they cannot race, and the token also
// carries a TTL: if this daemon is killed, Herdr expires the token instead of
// leaving a pane marked as blocked forever.
func (s *service) trackPane(req *hitl.Request, waiting <-chan struct{}) {
	if !s.cfg.Herdr.PaneTokensEnabled() || req.Origin.PaneID == "" || !s.herdr.Available() {
		return
	}
	paneID := req.Origin.PaneID
	value := fmt.Sprintf("? %d", s.broker.PendingCount()+1)
	ttl := req.Timeout

	s.herdrWG.Add(1)
	go func() {
		defer s.herdrWG.Done()
		if err := s.herdr.SetPaneToken(context.Background(), paneID, paneTokenName, value, ttl); err != nil {
			s.log.Debug("set pane token failed", "pane_id", paneID, "error", err)
		}
		<-waiting
		if err := s.herdr.ClearPaneToken(context.Background(), paneID, paneTokenName); err != nil {
			s.log.Debug("clear pane token failed", "pane_id", paneID, "error", err)
		}
	}()
}

// toast raises a Herdr notification without delaying the messenger delivery:
// the Herdr CLI is a subprocess, and an ask must not wait on a toast.
func (s *service) toast(title, body, sound string) {
	if !s.cfg.Herdr.NotificationsEnabled() || !s.herdr.Available() {
		return
	}
	if strings.TrimSpace(title) == "" {
		title = defaultToastTitle
	}
	s.herdrWG.Add(1)
	go func() {
		defer s.herdrWG.Done()
		if err := s.herdr.Notify(context.Background(), title, body, sound); err != nil {
			s.log.Debug("herdr notification failed", "error", err)
		}
	}()
}

// wait blocks until the outstanding Herdr callbacks finish. Each one is
// bounded by herdrctl's own exec timeout, so this cannot hang a shutdown.
func (s *service) wait() { s.herdrWG.Wait() }

// enter records that the daemon is busy and returns the matching leave
// function. The idle watchdog reads this accounting, so `pending` polls from a
// CLI count as activity just like a blocked ask does.
func (s *service) enter() func() {
	s.mu.Lock()
	s.inflight++
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		s.inflight--
		if s.inflight == 0 {
			s.idleFrom = s.now()
		}
		s.mu.Unlock()
	}
}

// idle reports whether the daemon has had nothing to do for at least window.
func (s *service) idle(window time.Duration) bool {
	if s.broker.PendingCount() > 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight > 0 {
		return false
	}
	return s.now().Sub(s.idleFrom) >= window
}

// watchIdle stops the daemon once it has been idle for window. It is off by
// default: reconnecting to Discord costs an IDENTIFY, and those are capped at
// 1000 per 24 hours with a token reset as the penalty.
func (s *service) watchIdle(ctx context.Context, window time.Duration, stop func()) {
	ticker := time.NewTicker(idleInterval(window))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.idle(window) {
				s.log.Info("idle shutdown", "idle_for", window)
				stop()
				return
			}
		}
	}
}

// defaultToastTitle is used when a question has a body but no title.
const defaultToastTitle = "Human decision needed"

// askSummary describes a posted question for a Herdr toast.
func askSummary(req *hitl.Request) string {
	parts := make([]string, 0, 3)
	if origin := req.Origin.Label(); origin != "" {
		parts = append(parts, origin)
	}
	if len(req.Choices) > 0 {
		parts = append(parts, fmt.Sprintf("%d choices", len(req.Choices)))
	}
	if deadline, ok := req.Deadline(); ok {
		parts = append(parts, "until "+deadline.Format("15:04"))
	}
	return strings.Join(parts, " · ")
}

// answerSummary describes a resolved question for a Herdr toast.
func answerSummary(ans *hitl.Answer) string {
	switch {
	case ans == nil:
		return ""
	case ans.Answered():
		text := ans.ChoiceLabel
		if text == "" {
			text = firstLine(ans.Text)
		}
		if who := ans.Responder.Display(); who != "" {
			return who + ": " + text
		}
		return text
	case ans.Reason != "":
		return string(ans.Status) + ": " + ans.Reason
	default:
		return string(ans.Status)
	}
}

// firstLine keeps a toast body to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// localUser names the human at this terminal, for answers typed into the CLI.
func localUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, key := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return "local"
}

// Compile-time proof that the daemon speaks the protocol the CLI expects.
var _ ipc.Handler = (*service)(nil)
