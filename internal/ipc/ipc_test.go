package ipc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

// fakeHandler is a scriptable daemon side.
type fakeHandler struct {
	mu sync.Mutex

	askEntered chan struct{}
	askResult  *hitl.Answer
	askErr     error
	// blockAsk, when set, makes Ask wait for the context instead of
	// returning, which is how a real ask behaves while a human thinks.
	blockAsk  bool
	askCtxErr error

	notified  []*ipc.AskParams
	answered  []*ipc.AnswerParams
	canceled  []*ipc.CancelParams
	shutdowns int
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{askEntered: make(chan struct{}, 1)}
}

func (f *fakeHandler) Ask(ctx context.Context, _ *ipc.AskParams) (*hitl.Answer, error) {
	select {
	case f.askEntered <- struct{}{}:
	default:
	}
	if f.blockAsk {
		<-ctx.Done()
		f.mu.Lock()
		f.askCtxErr = ctx.Err()
		f.mu.Unlock()
		return &hitl.Answer{Status: hitl.StatusCanceled, Reason: ctx.Err().Error()}, nil
	}
	return f.askResult, f.askErr
}

func (f *fakeHandler) Notify(_ context.Context, p *ipc.AskParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = append(f.notified, p)
	return nil
}

func (f *fakeHandler) Pending(context.Context) ([]*hitl.Request, error) {
	return []*hitl.Request{{ID: "abc123", Title: "Deploy?"}}, nil
}

func (f *fakeHandler) AnswerRequest(_ context.Context, p *ipc.AnswerParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, p)
	return nil
}

func (f *fakeHandler) CancelRequest(_ context.Context, p *ipc.CancelParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, p)
	return nil
}

func (f *fakeHandler) Status(context.Context) (*ipc.Status, error) {
	return &ipc.Status{PID: 4242, Version: "test", Transports: []string{"telegram"}}, nil
}

func (f *fakeHandler) Shutdown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	return nil
}

// startServer brings up a daemon on a throwaway endpoint and returns it.
func startServer(t *testing.T, h ipc.Handler) string {
	t.Helper()

	endpoint := testEndpoint(t)
	l, err := ipc.Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen(%q): %v", endpoint, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ipc.NewServer(h, nil).Serve(ctx, l); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return endpoint
}

func testEndpoint(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\herdr-hitl-test-%d-%s`, time.Now().UnixNano(), t.Name())
	}
	// t.TempDir() is short enough on every supported platform to stay inside
	// the sockaddr_un path limit.
	return filepath.Join(t.TempDir(), "d.sock")
}

func TestAskRoundTrip(t *testing.T) {
	t.Parallel()

	h := newFakeHandler()
	h.askResult = &hitl.Answer{
		RequestID: "abc123",
		Status:    hitl.StatusAnswered,
		ChoiceID:  "approve",
		Text:      "Approve",
	}
	endpoint := startServer(t, h)

	resp, err := ipc.Call(t.Context(), endpoint, &ipc.Request{
		Op: ipc.OpAsk,
		Ask: &ipc.AskParams{
			Body:    "Ship it?",
			Choices: []hitl.Choice{{ID: "approve", Label: "Approve"}},
			Timeout: ipc.Duration(30 * time.Second),
		},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Answer == nil || resp.Answer.ChoiceID != "approve" {
		t.Fatalf("answer = %+v", resp.Answer)
	}
}

func TestTimeoutTravelsAsAnAnswerNotAnError(t *testing.T) {
	t.Parallel()

	// A deadline that passes is a normal outcome of a successful exchange:
	// the CLI needs the reason and the request id to report it well.
	h := newFakeHandler()
	h.askResult = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusTimeout, Reason: "no answer within 1s"}
	endpoint := startServer(t, h)

	resp, err := ipc.Call(t.Context(), endpoint, &ipc.Request{Op: ipc.OpAsk, Ask: &ipc.AskParams{Body: "?"}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Answer.Status != hitl.StatusTimeout {
		t.Fatalf("status = %q, want timeout", resp.Answer.Status)
	}
	if !errors.Is(resp.Answer.Err(), hitl.ErrTimeout) {
		t.Errorf("Err() = %v, want ErrTimeout", resp.Answer.Err())
	}
}

func TestErrorCodesSurviveTheProcessBoundary(t *testing.T) {
	t.Parallel()

	h := newFakeHandler()
	h.askErr = fmt.Errorf("%w: nothing configured", hitl.ErrNoTransport)
	endpoint := startServer(t, h)

	_, err := ipc.Call(t.Context(), endpoint, &ipc.Request{Op: ipc.OpAsk, Ask: &ipc.AskParams{Body: "?"}})
	if err == nil {
		t.Fatal("Call = nil, want an error")
	}
	if !errors.Is(err, hitl.ErrNoTransport) {
		t.Fatalf("err = %v, want it to unwrap to ErrNoTransport", err)
	}
	var wire *ipc.Error
	if !errors.As(err, &wire) || wire.Code != ipc.CodeNoTransport {
		t.Fatalf("err = %v, want ipc.Error with code no_transport", err)
	}
}

func TestClientDisconnectCancelsTheAsk(t *testing.T) {
	t.Parallel()

	// This is the invariant that keeps a killed agent from leaving a live
	// button in the chat: EOF on the client connection cancels the handler.
	h := newFakeHandler()
	h.blockAsk = true
	endpoint := startServer(t, h)

	ctx, cancel := context.WithCancel(t.Context())
	client, err := ipc.Dial(ctx, endpoint)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	go func() {
		<-h.askEntered
		cancel()
	}()

	if _, err := client.Do(ctx, &ipc.Request{Op: ipc.OpAsk, Ask: &ipc.AskParams{Body: "?"}}); err == nil {
		t.Fatal("Do = nil, want the cancellation error")
	}
	_ = client.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		got := h.askCtxErr
		h.mu.Unlock()
		if got != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("handler context was never canceled after the client hung up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOperations(t *testing.T) {
	t.Parallel()

	h := newFakeHandler()
	endpoint := startServer(t, h)

	pending, err := ipc.Call(t.Context(), endpoint, &ipc.Request{Op: ipc.OpPending})
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending.Pending) != 1 || pending.Pending[0].ID != "abc123" {
		t.Errorf("pending = %+v", pending.Pending)
	}

	if _, err := ipc.Call(t.Context(), endpoint, &ipc.Request{
		Op:     ipc.OpAnswer,
		Answer: &ipc.AnswerParams{RequestID: "abc123", Text: "go"},
	}); err != nil {
		t.Fatalf("answer: %v", err)
	}

	if _, err := ipc.Call(t.Context(), endpoint, &ipc.Request{
		Op:     ipc.OpCancel,
		Cancel: &ipc.CancelParams{RequestID: "abc123", Reason: "no longer needed"},
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	status, err := ipc.Call(t.Context(), endpoint, &ipc.Request{Op: ipc.OpStatus})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status == nil || status.Status.PID != 4242 {
		t.Errorf("status = %+v", status.Status)
	}

	if _, err := ipc.Call(t.Context(), endpoint, &ipc.Request{Op: ipc.OpShutdown}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.answered) != 1 || len(h.canceled) != 1 || h.shutdowns != 1 {
		t.Errorf("handler saw answered=%d canceled=%d shutdowns=%d",
			len(h.answered), len(h.canceled), h.shutdowns)
	}
}

func TestMalformedAndMissingParameters(t *testing.T) {
	t.Parallel()

	endpoint := startServer(t, newFakeHandler())

	tests := []struct {
		name string
		req  *ipc.Request
	}{
		{"unknown op", &ipc.Request{Op: "teleport"}},
		{"ask without params", &ipc.Request{Op: ipc.OpAsk}},
		{"notify without params", &ipc.Request{Op: ipc.OpNotify}},
		{"answer without params", &ipc.Request{Op: ipc.OpAnswer}},
		{"cancel without params", &ipc.Request{Op: ipc.OpCancel}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ipc.Call(t.Context(), endpoint, tc.req)
			var wire *ipc.Error
			if !errors.As(err, &wire) || wire.Code != ipc.CodeInvalid {
				t.Fatalf("err = %v, want an invalid-request error", err)
			}
		})
	}
}

func TestProbeAndListenGuardAgainstASecondDaemon(t *testing.T) {
	t.Parallel()

	endpoint := startServer(t, newFakeHandler())
	if !ipc.Probe(t.Context(), endpoint) {
		t.Fatal("Probe = false for a live daemon")
	}
	// Two pollers on one Telegram bot token steal each other's updates, so a
	// second daemon must refuse to start rather than race.
	if _, err := ipc.Listen(endpoint); !errors.Is(err, ipc.ErrAlreadyRunning) {
		t.Fatalf("second Listen = %v, want ErrAlreadyRunning", err)
	}
}

func TestListenReclaimsAStaleSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes leave no stale file behind")
	}
	t.Parallel()

	endpoint := filepath.Join(t.TempDir(), "d.sock")
	// Simulate a crashed daemon: the socket file exists, nothing answers.
	l, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	unixListener, ok := l.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener is %T, want *net.UnixListener", l)
	}
	unixListener.SetUnlinkOnClose(false)
	_ = l.Close()

	reclaimed, err := ipc.Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	_ = reclaimed.Close()
}

func TestProbeOnADeadEndpoint(t *testing.T) {
	t.Parallel()

	if ipc.Probe(t.Context(), testEndpoint(t)) {
		t.Error("Probe = true for an endpoint nobody is serving")
	}
}

func TestDurationJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		D ipc.Duration `json:"d"`
	}

	encoded, err := json.Marshal(payload{D: ipc.Duration(90 * time.Second)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) != `{"d":"1m30s"}` {
		t.Fatalf("Marshal = %s", encoded)
	}

	for _, raw := range []string{`{"d":"1m30s"}`, `{"d":90000000000}`} {
		var got payload
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", raw, err)
		}
		if time.Duration(got.D) != 90*time.Second {
			t.Errorf("Unmarshal(%s) = %s, want 1m30s", raw, got.D)
		}
	}

	var bad payload
	if err := json.Unmarshal([]byte(`{"d":"soon"}`), &bad); err == nil {
		t.Error("Unmarshal of a bogus duration = nil, want error")
	}
}
