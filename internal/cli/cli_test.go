package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: ExitOK},
		{name: "plain error", err: errors.New("boom"), want: ExitError},
		{name: "usage error", err: usagef("bad flag"), want: ExitUsage},
		{name: "runtime error", err: failf("boom"), want: ExitError},
		{name: "timeout sentinel", err: hitl.ErrTimeout, want: ExitTimeout},
		{name: "wrapped timeout", err: fmt.Errorf("ask: %w", hitl.ErrTimeout), want: ExitTimeout},
		{name: "timeout over the wire", err: ipc.NewError(hitl.ErrTimeout), want: ExitTimeout},
		{name: "cancel over the wire", err: ipc.NewError(hitl.ErrCanceled), want: ExitCanceled},
		{name: "context cancel", err: context.Canceled, want: ExitCanceled},
		{name: "unknown request", err: ipc.NewError(hitl.ErrUnknownRequest), want: ExitError},
		{
			name: "sentinel beats the wrapper code",
			err:  withCode(ExitError, fmt.Errorf("ask: %w", hitl.ErrTimeout)),
			want: ExitTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCode(tt.err); got != tt.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// fakeHandler is a daemon that answers however the test tells it to.
type fakeHandler struct {
	mu     sync.Mutex
	answer *hitl.Answer
	err    error
	asked  *ipc.AskParams

	notified *ipc.AskParams
	pending  []*hitl.Request
	status   *ipc.Status
}

func (f *fakeHandler) Ask(_ context.Context, p *ipc.AskParams) (*hitl.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = p
	return f.answer, f.err
}

func (f *fakeHandler) Notify(_ context.Context, p *ipc.AskParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = p
	return f.err
}

func (f *fakeHandler) Pending(context.Context) ([]*hitl.Request, error) { return f.pending, nil }

func (f *fakeHandler) AnswerRequest(context.Context, *ipc.AnswerParams) error { return f.err }

func (f *fakeHandler) CancelRequest(context.Context, *ipc.CancelParams) error { return f.err }

func (f *fakeHandler) Status(context.Context) (*ipc.Status, error) { return f.status, nil }

func (f *fakeHandler) Shutdown(context.Context) error { return nil }

func (f *fakeHandler) lastAsk(t *testing.T) *ipc.AskParams {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.asked == nil {
		t.Fatal("daemon received no ask")
	}
	return f.asked
}

// harness runs commands against a fake daemon on a private socket.
type harness struct {
	handler  *fakeHandler
	endpoint string
}

// newHarness starts a fake daemon and points the CLI at a scratch config.
func newHarness(t *testing.T) *harness {
	t.Helper()

	// A leaked messenger token in the environment would change what the
	// commands do, so the whole surface is neutralised.
	for _, key := range []string{
		"HITL_TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN",
		"HITL_TELEGRAM_CHAT_ID", "TELEGRAM_CHAT_ID",
		"HITL_DISCORD_BOT_TOKEN", "DISCORD_BOT_TOKEN",
		"HITL_DISCORD_CHANNEL_ID", "DISCORD_CHANNEL_ID",
		"HITL_DISCORD_USER_ID", "HITL_TRANSPORTS", "HITL_TIMEOUT",
		"HITL_LOG_LEVEL", "HITL_IDLE_SHUTDOWN", "HITL_AGENT",
		"HERDR_PLUGIN_CONTEXT_JSON", "HERDR_PANE_ID", "HERDR_TAB_ID",
		"HERDR_WORKSPACE_ID",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("HITL_CONFIG_DIR", t.TempDir())

	h := &harness{handler: &fakeHandler{}}
	h.endpoint = filepath.Join(t.TempDir(), "d.sock")

	l, err := ipc.Listen(h.endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ipc.NewServer(h.handler, discardLogger()).Serve(ctx, l)
	}()
	t.Cleanup(func() {
		cancel()
		_ = l.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("fake daemon did not stop")
		}
	})
	return h
}

// run executes one command, returning the exit code Main would produce.
func (h *harness) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	root, g := newRootCommand(BuildInfo{Version: "test", Commit: "abc1234", Date: "today"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"--socket", h.endpoint}, args...))

	err := root.Execute()
	code = exitCode(err)
	if err != nil && !g.ran {
		code = ExitUsage
	}
	if err != nil && !silenced(err) {
		fmt.Fprintln(&errBuf, "herdr-hitl:", err)
	}
	return code, out.String(), errBuf.String()
}

// discardLogger silences the fake daemon; the CLI's own output is the subject.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAskAnswered(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{
		RequestID: "abc123",
		Status:    hitl.StatusAnswered,
		ChoiceID:  "ship-it",
		Text:      "Ship it",
	}

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "-m", "staging is green", "-c", "Ship it", "-c", "Hold")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if stdout != "Ship it\n" {
		t.Fatalf("stdout = %q, want the answer text only", stdout)
	}

	ask := h.handler.lastAsk(t)
	if ask.Title != "Deploy?" || ask.Body != "staging is green" {
		t.Fatalf("ask = %+v, want the title and body forwarded", ask)
	}
	if len(ask.Choices) != 2 || ask.Choices[0].ID != "ship-it" || ask.Choices[1].ID != "hold" {
		t.Fatalf("choices = %+v, want slugified ids", ask.Choices)
	}
	if !ask.AllowFreeText {
		t.Fatal("free text should be allowed by default")
	}
	if ask.Origin.Agent != "agent" {
		t.Fatalf("origin agent = %q, want the default", ask.Origin.Agent)
	}
}

func TestAskJSONFormat(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "-o", "json")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	var got hitl.Answer
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not an Answer document: %v (%q)", err, stdout)
	}
	if got.Text != "yes" || got.Status != hitl.StatusAnswered {
		t.Fatalf("answer = %+v", got)
	}
}

func TestAskTimeout(t *testing.T) {
	h := newHarness(t)
	h.handler.err = hitl.ErrTimeout

	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "--timeout", "1s")
	if code != ExitTimeout {
		t.Fatalf("exit code = %d, want %d", code, ExitTimeout)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing on a timeout", stdout)
	}
	if !strings.Contains(stderr, "no answer within 1s") {
		t.Fatalf("stderr = %q, want a timeout diagnostic", stderr)
	}
	if n := strings.Count(stderr, "herdr-hitl:"); n != 1 {
		t.Fatalf("stderr = %q, want exactly one diagnostic line, got %d", stderr, n)
	}
}

// TestAskTimeoutFromAnswer covers the other timeout shape: the daemon reports
// success and hands back a non-answered Answer.
func TestAskTimeoutFromAnswer(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusTimeout}

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "--timeout", "1s")
	if code != ExitTimeout {
		t.Fatalf("exit code = %d, want %d", code, ExitTimeout)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing on a timeout", stdout)
	}
}

func TestAskTimeoutWithDefault(t *testing.T) {
	h := newHarness(t)
	h.handler.err = hitl.ErrTimeout

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "--timeout", "1s", "--default", "hold")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if stdout != "hold\n" {
		t.Fatalf("stdout = %q, want the default answer", stdout)
	}
}

// TestAskEmptyDefault proves the fallback is driven by the flag being present,
// not by it being non-empty: --default "" means "answer with nothing".
func TestAskEmptyDefault(t *testing.T) {
	h := newHarness(t)
	h.handler.err = hitl.ErrTimeout

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "--default", "")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if stdout != "\n" {
		t.Fatalf("stdout = %q, want an empty answer line", stdout)
	}
}

func TestAskCanceled(t *testing.T) {
	h := newHarness(t)
	h.handler.err = hitl.ErrCanceled

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?")
	if code != ExitCanceled {
		t.Fatalf("exit code = %d, want %d", code, ExitCanceled)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing on a cancel", stdout)
	}
}

// TestAskCanceledIgnoresDefault: --default papers over a deadline, never over
// a human or an agent withdrawing the question.
func TestAskCanceledIgnoresDefault(t *testing.T) {
	h := newHarness(t)
	h.handler.err = hitl.ErrCanceled

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "--default", "hold")
	if code != ExitCanceled {
		t.Fatalf("exit code = %d, want %d", code, ExitCanceled)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing on a cancel", stdout)
	}
}

func TestAskTransportFailure(t *testing.T) {
	h := newHarness(t)
	h.handler.err = hitl.ErrNoTransport

	code, _, _ := h.run(t, "ask", "-t", "Deploy?")
	if code != ExitError {
		t.Fatalf("exit code = %d, want %d", code, ExitError)
	}
}

func TestAskUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"ask", "--nope"}},
		{name: "unknown format", args: []string{"ask", "-t", "x", "-o", "yaml"}},
		{name: "unknown transport", args: []string{"ask", "-t", "x", "--transport", "sms"}},
		{name: "no choices and no free text", args: []string{"ask", "-t", "x", "--free=false"}},
		{name: "duplicate choice ids", args: []string{"ask", "-t", "x", "-c", "Yes", "-c", "yes"}},
		{name: "unknown primary id", args: []string{"ask", "-t", "x", "-c", "Yes", "--primary", "no"}},
		{name: "missing attachment", args: []string{"ask", "-t", "x", "-a", "/nonexistent/file.png"}},
		{name: "unknown subcommand", args: []string{"frobnicate"}},
		{name: "answer without a choice or text", args: []string{"answer", "abc123"}},
		{name: "answer with both", args: []string{"answer", "abc123", "--choice", "y", "--text", "z"}},
		{name: "answer without an id", args: []string{"answer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			code, stdout, _ := h.run(t, tt.args...)
			if code != ExitUsage {
				t.Fatalf("exit code = %d, want %d", code, ExitUsage)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing on a usage error", stdout)
			}
		})
	}
}

func TestAskBodyFromStdin(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{Status: hitl.StatusAnswered, Text: "ok"}

	root, _ := newRootCommand(BuildInfo{Version: "test"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("piped question"))
	root.SetArgs([]string{"--socket", h.endpoint, "ask", "-t", "Deploy?", "-m", "-"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if body := h.handler.lastAsk(t).Body; body != "piped question" {
		t.Fatalf("body = %q, want the piped text", body)
	}
}

func TestAskAgentOverride(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{Status: hitl.StatusAnswered, Text: "ok"}

	h.run(t, "ask", "-t", "x", "--agent", "claude")
	if agent := h.handler.lastAsk(t).Origin.Agent; agent != "claude" {
		t.Fatalf("origin agent = %q, want %q", agent, "claude")
	}

	t.Setenv("HITL_AGENT", "codex")
	h.run(t, "ask", "-t", "x")
	if agent := h.handler.lastAsk(t).Origin.Agent; agent != "codex" {
		t.Fatalf("origin agent = %q, want %q", agent, "codex")
	}
}

func TestNotify(t *testing.T) {
	h := newHarness(t)

	code, stdout, _ := h.run(t, "notify", "-t", "Build finished", "-m", "all green")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing", stdout)
	}
	h.handler.mu.Lock()
	defer h.handler.mu.Unlock()
	if h.handler.notified == nil {
		t.Fatal("daemon received no notification")
	}
	if h.handler.notified.AllowFreeText || len(h.handler.notified.Choices) != 0 {
		t.Fatalf("notification = %+v, want no reply surface", h.handler.notified)
	}
}

func TestPendingTable(t *testing.T) {
	h := newHarness(t)
	h.handler.pending = []*hitl.Request{{
		ID:        "abc123def456",
		Title:     "Deploy?",
		Choices:   []hitl.Choice{{ID: "yes", Label: "Yes"}},
		Origin:    hitl.Origin{Agent: "claude", Cwd: "/tmp/repo", Host: "box"},
		CreatedAt: time.Now().Add(-90 * time.Second),
	}}

	code, stdout, _ := h.run(t, "pending")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	for _, want := range []string{"abc123def456", "1m30s", "claude", "Deploy?"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestPendingWithoutDaemon(t *testing.T) {
	h := newHarness(t)
	h.endpoint = filepath.Join(t.TempDir(), "absent.sock")

	code, _, stderr := h.run(t, "pending")
	if code != ExitError {
		t.Fatalf("exit code = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "daemon is not running") {
		t.Fatalf("stderr = %q, want a not-running diagnostic", stderr)
	}
}

func TestVersionJSON(t *testing.T) {
	h := newHarness(t)

	code, stdout, _ := h.run(t, "version", "-o", "json")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	var got versionInfo
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if got.Version != "test" || got.Commit != "abc1234" {
		t.Fatalf("version info = %+v, want the injected build info", got)
	}
}

func TestConfigShowMasksSecrets(t *testing.T) {
	h := newHarness(t)
	dir := os.Getenv("HITL_CONFIG_DIR")
	const token = "123456789:AAHsupersecretvalue"
	body := "[telegram]\nbot_token = \"" + token + "\"\nchat_id = \"42\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := h.run(t, "config", "show")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if strings.Contains(stdout, token) {
		t.Fatalf("config show leaked the token:\n%s", stdout)
	}
	if !strings.Contains(stdout, mask(token)) {
		t.Fatalf("stdout = %q, want the masked fingerprint %q", stdout, mask(token))
	}
}

func TestConfigInitDoesNotOverwrite(t *testing.T) {
	h := newHarness(t)
	dir := os.Getenv("HITL_CONFIG_DIR")

	if code, _, _ := h.run(t, "config", "init"); code != ExitOK {
		t.Fatalf("first init exit code = %d, want %d", code, ExitOK)
	}
	file := filepath.Join(dir, "config.toml")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %v, want 0600", perm)
	}
	if code, _, stderr := h.run(t, "config", "init"); code != ExitError {
		t.Fatalf("second init exit code = %d, want %d (%s)", code, ExitError, stderr)
	}
	if code, _, _ := h.run(t, "config", "init", "--force"); code != ExitOK {
		t.Fatalf("forced init exit code = %d, want %d", code, ExitOK)
	}
}

// TestRootCommandTree guards the frozen command surface.
func TestRootCommandTree(t *testing.T) {
	t.Parallel()

	root := NewRootCommand(BuildInfo{})
	want := []string{
		"ask", "notify", "pending", "answer", "cancel", "serve",
		"daemon", "doctor", "config", "install-cli", "version",
	}
	have := make(map[string]*cobra.Command, len(root.Commands()))
	for _, c := range root.Commands() {
		have[c.Name()] = c
	}
	for _, name := range want {
		if _, ok := have[name]; !ok {
			t.Errorf("missing command %q", name)
		}
	}
	for _, sub := range []string{"start", "stop", "status", "restart"} {
		if !hasSub(have["daemon"], sub) {
			t.Errorf("missing daemon subcommand %q", sub)
		}
	}
	for _, sub := range []string{"path", "show", "init"} {
		if !hasSub(have["config"], sub) {
			t.Errorf("missing config subcommand %q", sub)
		}
	}
}

func hasSub(cmd *cobra.Command, name string) bool {
	if cmd == nil {
		return false
	}
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}
