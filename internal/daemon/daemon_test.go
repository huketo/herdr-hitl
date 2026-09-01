package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
	"github.com/huketo/herdr-hitl/internal/paths"
	"github.com/huketo/herdr-hitl/internal/transport"
)

// discardLogger keeps daemon logs out of the test output. Goroutines outlive
// individual tests here, and t.Log from a finished test panics.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeTransport is an in-process messenger: it records what the broker posts
// and lets the test play the human by calling the resolver.
type fakeTransport struct {
	name     string
	resolver hitl.Resolver
	posted   chan *hitl.Request
	settled  chan *hitl.Answer
	startErr error

	mu      sync.Mutex
	started bool
	closed  bool
}

func newFakeTransport(name string, resolver hitl.Resolver) *fakeTransport {
	return &fakeTransport{
		name:     name,
		resolver: resolver,
		posted:   make(chan *hitl.Request, 8),
		settled:  make(chan *hitl.Answer, 8),
	}
}

func (f *fakeTransport) Name() string { return f.name }

func (f *fakeTransport) Post(_ context.Context, req *hitl.Request) error {
	f.posted <- req
	return nil
}

func (f *fakeTransport) Settle(_ context.Context, _ *hitl.Request, ans *hitl.Answer) error {
	f.settled <- ans
	return nil
}

func (f *fakeTransport) Start(context.Context) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return nil
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeTransport) Describe() string { return f.name + ": in-process fake" }

func (f *fakeTransport) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// answer plays the human, pushing a choice back through the resolver.
func (f *fakeTransport) answer(t *testing.T, req *hitl.Request, choiceID string) {
	t.Helper()
	f.resolve(t, &hitl.Answer{RequestID: req.ID, ChoiceID: choiceID})
}

// reply plays the human typing a free-text answer.
func (f *fakeTransport) reply(t *testing.T, req *hitl.Request, text string) {
	t.Helper()
	f.resolve(t, &hitl.Answer{RequestID: req.ID, Text: text})
}

func (f *fakeTransport) resolve(t *testing.T, ans *hitl.Answer) {
	t.Helper()
	ans.Responder = hitl.Responder{Transport: f.name, UserID: "42", Username: "human"}
	if err := f.resolver.Resolve(ans); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

// nextPost waits for the broker to deliver a question.
func (f *fakeTransport) nextPost(t *testing.T) *hitl.Request {
	t.Helper()
	select {
	case req := <-f.posted:
		return req
	case <-time.After(5 * time.Second):
		t.Fatalf("transport %s received no question", f.name)
		return nil
	}
}

// nextSettle waits for the broker to strip the controls off a message.
func (f *fakeTransport) nextSettle(t *testing.T) *hitl.Answer {
	t.Helper()
	select {
	case ans := <-f.settled:
		return ans
	case <-time.After(5 * time.Second):
		t.Fatalf("transport %s was never settled", f.name)
		return nil
	}
}

func factoryOf(ts ...transport.Transport) TransportFactory {
	return func(*config.Config, hitl.Resolver, *slog.Logger) ([]transport.Transport, error) {
		return ts, nil
	}
}

// stateEndpoint points the whole package at a throwaway state directory and
// returns the endpoint the daemon will bind. Going through paths.Socket keeps
// the test portable: a Unix socket path here, a named pipe on Windows.
func stateEndpoint(t *testing.T) string {
	t.Helper()
	t.Setenv(paths.EnvStateDir, t.TempDir())
	endpoint, err := paths.Socket()
	if err != nil {
		t.Fatalf("resolve socket: %v", err)
	}
	return endpoint
}

// runningDaemon starts Run in a goroutine and waits until it accepts
// connections. The returned stop function cancels it and asserts a clean exit.
func runningDaemon(t *testing.T, cfg *config.Config, endpoint string, factory TransportFactory) (stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config:        cfg,
			Endpoint:      endpoint,
			Version:       "test",
			Log:           discardLogger(),
			NewTransports: factory,
		})
	}()

	if err := waitForDaemon(ctx, endpoint, 5*time.Second); err != nil {
		cancel()
		t.Fatalf("daemon never came up: %v (run: %v)", err, <-done)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run returned %v, want nil", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("daemon did not stop")
			}
		})
	}
}

// askResult carries an ask that runs on its own goroutine while the test plays
// the human.
type askResult struct {
	resp *ipc.Response
	err  error
}

func askAsync(ctx context.Context, endpoint string, p *ipc.AskParams) <-chan askResult {
	out := make(chan askResult, 1)
	go func() {
		resp, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpAsk, Ask: p})
		out <- askResult{resp: resp, err: err}
	}()
	return out
}

func waitAsk(t *testing.T, ch <-chan askResult) askResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("ask never returned")
		return askResult{}
	}
}

func TestRunAnswersAsk(t *testing.T) {
	endpoint := stateEndpoint(t)
	cfg := config.Default()

	var fake *fakeTransport
	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		fake = newFakeTransport("fake", resolver)
		return []transport.Transport{fake}, nil
	}
	stop := runningDaemon(t, cfg, endpoint, factory)
	defer stop()

	ctx := context.Background()
	pending := askAsync(ctx, endpoint, &ipc.AskParams{
		Title: "Deploy?",
		Body:  "staging is green",
		Choices: []hitl.Choice{
			{ID: "yes", Label: "Ship it", Style: hitl.StylePrimary},
			{ID: "no", Label: "Hold"},
		},
		Origin: hitl.Origin{Agent: "claude", Cwd: "/tmp/repo"},
	})

	req := fake.nextPost(t)
	if req.Timeout != config.DefaultTimeout {
		t.Errorf("timeout = %v, want the config default %v", req.Timeout, config.DefaultTimeout)
	}
	if req.Origin.Agent != "claude" {
		t.Errorf("origin agent = %q, want claude", req.Origin.Agent)
	}

	// The question must be visible as pending while the human thinks.
	statusResp, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpStatus})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := statusResp.Status.Pending; got != 1 {
		t.Errorf("pending = %d, want 1", got)
	}
	if got := statusResp.Status.Transports; len(got) != 1 || got[0] != "fake" {
		t.Errorf("transports = %v, want [fake]", got)
	}
	if statusResp.Status.Version != "test" || statusResp.Status.PID != os.Getpid() {
		t.Errorf("status = %+v, want version test and this pid", statusResp.Status)
	}

	fake.answer(t, req, "yes")

	res := waitAsk(t, pending)
	if res.err != nil {
		t.Fatalf("ask: %v", res.err)
	}
	ans := res.resp.Answer
	if !ans.Answered() {
		t.Fatalf("status = %q, want answered", ans.Status)
	}
	if ans.ChoiceID != "yes" || ans.ChoiceLabel != "Ship it" || ans.Text != "Ship it" {
		t.Errorf("answer = %+v, want the resolved choice", ans)
	}
	if ans.Responder.Transport != "fake" || ans.Responder.Username != "human" {
		t.Errorf("responder = %+v, want the fake transport's human", ans.Responder)
	}

	if settled := fake.nextSettle(t); settled.Status != hitl.StatusAnswered {
		t.Errorf("settled with %q, want answered", settled.Status)
	}

	stop()
	if !fake.isClosed() {
		t.Error("transport was not closed on shutdown")
	}
}

func TestClientDisconnectCancelsAsk(t *testing.T) {
	endpoint := stateEndpoint(t)

	var fake *fakeTransport
	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		fake = newFakeTransport("fake", resolver)
		return []transport.Transport{fake}, nil
	}
	stop := runningDaemon(t, config.Default(), endpoint, factory)
	defer stop()

	askCtx, abandon := context.WithCancel(context.Background())
	pending := askAsync(askCtx, endpoint, &ipc.AskParams{
		Title:         "Still there?",
		AllowFreeText: true,
	})
	req := fake.nextPost(t)

	// The agent gets Ctrl-C'd: the client hangs up mid-ask.
	abandon()
	waitAsk(t, pending)

	settled := fake.nextSettle(t)
	if settled.Status != hitl.StatusCanceled {
		t.Fatalf("settled with %q, want canceled", settled.Status)
	}
	if settled.RequestID != req.ID {
		t.Errorf("settled request %q, want %q", settled.RequestID, req.ID)
	}
	if err := settled.Err(); !errors.Is(err, hitl.ErrCanceled) {
		t.Errorf("Err() = %v, want ErrCanceled", err)
	}

	// The withdrawn question must not linger in the pending list.
	resp, err := ipc.Call(context.Background(), endpoint, &ipc.Request{Op: ipc.OpPending})
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(resp.Pending) != 0 {
		t.Errorf("pending = %v, want empty", resp.Pending)
	}
}

func TestAskTimesOut(t *testing.T) {
	endpoint := stateEndpoint(t)

	var fake *fakeTransport
	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		fake = newFakeTransport("fake", resolver)
		return []transport.Transport{fake}, nil
	}
	stop := runningDaemon(t, config.Default(), endpoint, factory)
	defer stop()

	res := waitAsk(t, askAsync(context.Background(), endpoint, &ipc.AskParams{
		Title:         "Quick!",
		AllowFreeText: true,
		Timeout:       ipc.Duration(80 * time.Millisecond),
	}))
	if res.err != nil {
		t.Fatalf("ask: %v", res.err)
	}
	if got := res.resp.Answer.Status; got != hitl.StatusTimeout {
		t.Fatalf("status = %q, want timeout", got)
	}
	if err := res.resp.Answer.Err(); !errors.Is(err, hitl.ErrTimeout) {
		t.Errorf("Err() = %v, want ErrTimeout", err)
	}
	if settled := fake.nextSettle(t); settled.Status != hitl.StatusTimeout {
		t.Errorf("settled with %q, want timeout", settled.Status)
	}
}

func TestCLIAnswerAndCancel(t *testing.T) {
	endpoint := stateEndpoint(t)

	var fake *fakeTransport
	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		fake = newFakeTransport("fake", resolver)
		return []transport.Transport{fake}, nil
	}
	stop := runningDaemon(t, config.Default(), endpoint, factory)
	defer stop()

	ctx := context.Background()

	// Answered from the terminal instead of the messenger.
	pending := askAsync(ctx, endpoint, &ipc.AskParams{Title: "Proceed?", AllowFreeText: true})
	req := fake.nextPost(t)
	if _, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpAnswer, Answer: &ipc.AnswerParams{
		RequestID: req.ID,
		Text:      "go ahead",
		Responder: "huke",
	}}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	res := waitAsk(t, pending)
	if res.err != nil {
		t.Fatalf("ask: %v", res.err)
	}
	if res.resp.Answer.Text != "go ahead" || res.resp.Answer.Responder.Transport != cliTransport {
		t.Errorf("answer = %+v, want the CLI answer", res.resp.Answer)
	}
	fake.nextSettle(t)

	// Canceled from the terminal.
	pending = askAsync(ctx, endpoint, &ipc.AskParams{Title: "Proceed?", AllowFreeText: true})
	req = fake.nextPost(t)
	if _, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpCancel, Cancel: &ipc.CancelParams{
		RequestID: req.ID,
		Reason:    "no longer needed",
	}}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res = waitAsk(t, pending)
	if res.err != nil {
		t.Fatalf("ask: %v", res.err)
	}
	if res.resp.Answer.Status != hitl.StatusCanceled || res.resp.Answer.Reason != "no longer needed" {
		t.Errorf("answer = %+v, want canceled with the reason", res.resp.Answer)
	}
	fake.nextSettle(t)

	// Unknown ids must be reported as such, not swallowed.
	_, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpAnswer, Answer: &ipc.AnswerParams{
		RequestID: "deadbeef0000", Text: "hi",
	}})
	if !errors.Is(err, hitl.ErrUnknownRequest) {
		t.Errorf("answer for a stale id = %v, want ErrUnknownRequest", err)
	}
}

func TestSecondDaemonRefusesTheEndpoint(t *testing.T) {
	endpoint := stateEndpoint(t)

	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		return []transport.Transport{newFakeTransport("fake", resolver)}, nil
	}
	stop := runningDaemon(t, config.Default(), endpoint, factory)
	defer stop()

	err := Run(context.Background(), Options{
		Config:        config.Default(),
		Endpoint:      endpoint,
		Log:           discardLogger(),
		NewTransports: factory,
	})
	if !errors.Is(err, ipc.ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ipc.ErrAlreadyRunning", err)
	}
}

func TestRunFailsWhenEveryTransportFailsToStart(t *testing.T) {
	endpoint := stateEndpoint(t)

	broken := newFakeTransport("broken", nil)
	broken.startErr = errors.New("bad token")

	err := Run(context.Background(), Options{
		Config:        config.Default(),
		Endpoint:      endpoint,
		Log:           discardLogger(),
		NewTransports: factoryOf(broken),
	})
	if !errors.Is(err, hitl.ErrNoTransport) {
		t.Fatalf("Run = %v, want ErrNoTransport", err)
	}
	if !broken.isClosed() {
		t.Error("a transport that failed to start was not closed")
	}
}

func TestRunSurvivesOneBrokenTransport(t *testing.T) {
	endpoint := stateEndpoint(t)

	broken := newFakeTransport("broken", nil)
	broken.startErr = errors.New("bad token")
	var good *fakeTransport
	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		good = newFakeTransport("good", resolver)
		return []transport.Transport{broken, good}, nil
	}
	stop := runningDaemon(t, config.Default(), endpoint, factory)
	defer stop()

	resp, err := ipc.Call(context.Background(), endpoint, &ipc.Request{Op: ipc.OpStatus})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := resp.Status.Transports; len(got) != 1 || got[0] != "good" {
		t.Fatalf("transports = %v, want [good]", got)
	}
}

func TestShutdownOpStopsTheDaemon(t *testing.T) {
	endpoint := stateEndpoint(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config:   config.Default(),
			Endpoint: endpoint,
			Log:      discardLogger(),
			NewTransports: func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
				return []transport.Transport{newFakeTransport("fake", resolver)}, nil
			},
		})
	}()
	if err := waitForDaemon(ctx, endpoint, 5*time.Second); err != nil {
		t.Fatalf("daemon never came up: %v", err)
	}

	// The response must be written before the process winds down.
	if _, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpShutdown}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon ignored the shutdown request")
	}

	// A leftover socket file makes the next start look like a running daemon
	// until it is probed, so shutdown has to unlink it.
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(endpoint); !os.IsNotExist(err) {
			t.Errorf("stat %s = %v, want the socket to be removed", endpoint, err)
		}
	}
}

func TestIdleShutdown(t *testing.T) {
	endpoint := stateEndpoint(t)

	cfg := config.Default()
	cfg.Daemon.IdleShutdown = config.Duration(200 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config:   cfg,
			Endpoint: endpoint,
			Log:      discardLogger(),
			NewTransports: func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
				return []transport.Transport{newFakeTransport("fake", resolver)}, nil
			},
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("idle daemon stayed resident")
	}
}

func TestIdleShutdownWaitsForAPendingQuestion(t *testing.T) {
	endpoint := stateEndpoint(t)

	cfg := config.Default()
	cfg.Daemon.IdleShutdown = config.Duration(150 * time.Millisecond)

	var fake *fakeTransport
	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		fake = newFakeTransport("fake", resolver)
		return []transport.Transport{fake}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Config: cfg, Endpoint: endpoint, Log: discardLogger(), NewTransports: factory})
	}()
	if err := waitForDaemon(ctx, endpoint, 5*time.Second); err != nil {
		t.Fatalf("daemon never came up: %v", err)
	}

	pending := askAsync(ctx, endpoint, &ipc.AskParams{Title: "Wait for me", AllowFreeText: true})
	req := fake.nextPost(t)

	// Well past the idle window, the daemon must still be serving.
	time.Sleep(500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("daemon exited with a question outstanding: %v", err)
	default:
	}

	fake.reply(t, req, "done waiting")
	if res := waitAsk(t, pending); res.err != nil {
		t.Fatalf("ask: %v", res.err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon stayed resident after the question resolved")
	}
}
