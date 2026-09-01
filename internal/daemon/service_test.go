package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/herdrctl"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

func testService(t *testing.T, cfg *config.Config) *service {
	t.Helper()
	if cfg == nil {
		cfg = config.Default()
	}
	return newService(cfg, hitl.NewBroker(discardLogger()), discardLogger(), "test.sock", "test")
}

// herdrCall records one invocation of the Herdr CLI seam.
type herdrCall struct {
	op    string
	args  []string
	ttl   time.Duration
	sound string
}

// fakeHerdr stands in for the Herdr CLI.
type fakeHerdr struct {
	mu    sync.Mutex
	calls []herdrCall
}

func (f *fakeHerdr) Available() bool { return true }

func (f *fakeHerdr) Notify(_ context.Context, title, body, sound string) error {
	f.record(herdrCall{op: "notify", args: []string{title, body}, sound: sound})
	return nil
}

func (f *fakeHerdr) SetPaneToken(_ context.Context, paneID, name, value string, ttl time.Duration) error {
	f.record(herdrCall{op: "set-token", args: []string{paneID, name, value}, ttl: ttl})
	return nil
}

func (f *fakeHerdr) ClearPaneToken(_ context.Context, paneID, name string) error {
	f.record(herdrCall{op: "clear-token", args: []string{paneID, name}})
	return nil
}

func (f *fakeHerdr) record(c herdrCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeHerdr) snapshot() []herdrCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]herdrCall(nil), f.calls...)
}

func TestAskDrivesHerdrCallbacks(t *testing.T) {
	t.Parallel()

	svc := testService(t, nil)
	herdr := &fakeHerdr{}
	svc.herdr = herdr

	fake := newFakeTransport("fake", svc.broker)
	svc.broker.Register(fake)

	type result struct {
		ans *hitl.Answer
		err error
	}
	out := make(chan result, 1)
	go func() {
		ans, err := svc.Ask(context.Background(), &ipc.AskParams{
			Title:   "Deploy?",
			Choices: []hitl.Choice{{ID: "yes", Label: "Ship it"}},
			Timeout: ipc.Duration(time.Minute),
			Origin:  hitl.Origin{Agent: "claude", PaneID: "pane-7"},
		})
		out <- result{ans: ans, err: err}
	}()

	req := fake.nextPost(t)
	fake.answer(t, req, "yes")
	if res := <-out; res.err != nil || !res.ans.Answered() {
		t.Fatalf("Ask = (%+v, %v), want an answer", res.ans, res.err)
	}
	svc.wait()

	var sawRequestToast, sawDoneToast, sawSet, sawClear bool
	for _, c := range herdr.snapshot() {
		switch {
		case c.op == "notify" && c.sound == herdrctl.SoundRequest:
			sawRequestToast = true
		case c.op == "notify" && c.sound == herdrctl.SoundDone:
			sawDoneToast = true
			if !strings.Contains(c.args[1], "Ship it") {
				t.Errorf("done toast body = %q, want the chosen label", c.args[1])
			}
		case c.op == "set-token":
			sawSet = true
			want := []string{"pane-7", paneTokenName, "? 1"}
			if !slices.Equal(c.args, want) {
				t.Errorf("pane token = %v, want %v", c.args, want)
			}
			if c.ttl != time.Minute {
				t.Errorf("pane token ttl = %v, want the request timeout", c.ttl)
			}
		case c.op == "clear-token":
			sawClear = true
			if !sawSet {
				t.Error("pane token cleared before it was set")
			}
		}
	}
	if !sawRequestToast || !sawDoneToast || !sawSet || !sawClear {
		t.Errorf("calls = %+v, want a request toast, a done toast, and a set/clear token pair",
			herdr.snapshot())
	}
}

func TestAskSkipsHerdrCallbacksWhenDisabled(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	off := false
	cfg.Herdr.Notifications = &off
	cfg.Herdr.PaneTokens = &off

	svc := testService(t, cfg)
	herdr := &fakeHerdr{}
	svc.herdr = herdr

	fake := newFakeTransport("fake", svc.broker)
	svc.broker.Register(fake)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := svc.Ask(context.Background(), &ipc.AskParams{
			Title:   "Deploy?",
			Choices: []hitl.Choice{{ID: "yes", Label: "Ship it"}},
			Origin:  hitl.Origin{PaneID: "pane-7"},
		}); err != nil {
			t.Errorf("Ask: %v", err)
		}
	}()
	req := fake.nextPost(t)
	fake.answer(t, req, "yes")
	<-done
	svc.wait()

	if calls := herdr.snapshot(); len(calls) != 0 {
		t.Errorf("herdr was called with notifications and pane tokens off: %+v", calls)
	}
}

func TestRequestAppliesConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Timeout = config.Duration(7 * time.Minute)
	cfg.Transports = []string{config.TransportTelegram}
	cfg.Telegram.BotToken = "t"
	cfg.Telegram.ChatID = "1"

	tests := []struct {
		name           string
		params         *ipc.AskParams
		wantTimeout    time.Duration
		wantTransports []string
	}{
		{
			name:           "empty fields take the config defaults",
			params:         &ipc.AskParams{Title: "q", AllowFreeText: true},
			wantTimeout:    7 * time.Minute,
			wantTransports: []string{config.TransportTelegram},
		},
		{
			name: "explicit fields win",
			params: &ipc.AskParams{
				Title:         "q",
				AllowFreeText: true,
				Timeout:       ipc.Duration(90 * time.Second),
				Transports:    []string{config.TransportDiscord},
			},
			wantTimeout:    90 * time.Second,
			wantTransports: []string{config.TransportDiscord},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, err := testService(t, cfg).request(tt.params)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if req.Timeout != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", req.Timeout, tt.wantTimeout)
			}
			if len(req.Transports) != len(tt.wantTransports) || req.Transports[0] != tt.wantTransports[0] {
				t.Errorf("transports = %v, want %v", req.Transports, tt.wantTransports)
			}
			if req.ID == "" || req.CreatedAt.IsZero() {
				t.Errorf("request = %+v, want an id and a timestamp", req)
			}
		})
	}
}

func TestRequestResolvesAttachments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	doc := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(doc, []byte("# plan\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	svc := testService(t, nil)
	req, err := svc.request(&ipc.AskParams{Title: "q", AllowFreeText: true, Attachments: []string{doc}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if len(req.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(req.Attachments))
	}
	att := req.Attachments[0]
	if att.Filename != "plan.md" || att.Kind != hitl.KindDocument || att.Size == 0 {
		t.Errorf("attachment = %+v, want the resolved markdown document", att)
	}

	if _, err := svc.request(&ipc.AskParams{
		Title:       "q",
		Attachments: []string{filepath.Join(dir, "missing.png")},
	}); err == nil {
		t.Error("a missing attachment must fail the ask, not post a broken question")
	}
}

func TestIdleAccounting(t *testing.T) {
	t.Parallel()

	svc := testService(t, nil)
	now := time.Now()
	svc.now = func() time.Time { return now }
	svc.idleFrom = now.Add(-time.Minute)

	if !svc.idle(time.Second) {
		t.Fatal("a quiet daemon must count as idle")
	}

	leave := svc.enter()
	if svc.idle(time.Second) {
		t.Error("a daemon serving a call must not count as idle")
	}
	leave()
	if svc.idle(time.Second) {
		t.Error("the idle window must restart when a call finishes")
	}

	now = now.Add(2 * time.Second)
	if !svc.idle(time.Second) {
		t.Error("the daemon must go idle once the window elapses")
	}
}

func TestStatusReportsUptime(t *testing.T) {
	t.Parallel()

	svc := testService(t, nil)
	start := svc.start
	svc.now = func() time.Time { return start.Add(90 * time.Second) }

	st, err := svc.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Uptime != "1m30s" {
		t.Errorf("uptime = %q, want 1m30s", st.Uptime)
	}
	if st.Socket != "test.sock" || st.Version != "test" {
		t.Errorf("status = %+v, want the endpoint and version it was built with", st)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	svc := testService(t, nil)
	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	select {
	case <-svc.stop:
	default:
		t.Error("Shutdown did not signal Run")
	}
}

func TestAnswerSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ans  *hitl.Answer
		want string
	}{
		{
			name: "choice with responder",
			ans: &hitl.Answer{
				Status: hitl.StatusAnswered, ChoiceLabel: "Ship it",
				Responder: hitl.Responder{Transport: "telegram", Username: "huke"},
			},
			want: hitl.Responder{Transport: "telegram", Username: "huke"}.Display() + ": Ship it",
		},
		{
			name: "free text keeps one line",
			ans: &hitl.Answer{
				Status: hitl.StatusAnswered, Text: "yes\nbut check the migration",
				Responder: hitl.Responder{Transport: "cli", Username: "huke"},
			},
			want: hitl.Responder{Transport: "cli", Username: "huke"}.Display() + ": yes",
		},
		{
			name: "timeout carries the reason",
			ans:  &hitl.Answer{Status: hitl.StatusTimeout, Reason: "no answer within 30m"},
			want: "timeout: no answer within 30m",
		},
		{
			name: "canceled without a reason",
			ans:  &hitl.Answer{Status: hitl.StatusCanceled},
			want: "canceled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := answerSummary(tt.ans); got != tt.want {
				t.Errorf("answerSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdleInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		idle time.Duration
		want time.Duration
	}{
		{idle: time.Millisecond, want: minIdleInterval},
		{idle: 4 * time.Second, want: time.Second},
		{idle: 24 * time.Hour, want: maxIdleInterval},
	}
	for _, tt := range tests {
		if got := idleInterval(tt.idle); got != tt.want {
			t.Errorf("idleInterval(%v) = %v, want %v", tt.idle, got, tt.want)
		}
	}
}
