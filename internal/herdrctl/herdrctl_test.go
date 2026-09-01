package herdrctl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClient returns a client wired to a recording runner instead of exec.
func fakeClient(t *testing.T, err error) (*Client, *[][]string) {
	t.Helper()
	var calls [][]string
	c := &Client{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		bin: "/usr/bin/herdr",
		run: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return err
		},
	}
	return c, &calls
}

func TestNotifyArgv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		body  string
		sound string
		want  []string
	}{
		{
			name:  "title body and sound",
			title: "Deploy?",
			body:  "staging -> production",
			sound: SoundRequest,
			want: []string{
				"/usr/bin/herdr", "notification", "show", "Deploy?",
				"--sound", "request", "--body", "staging -> production",
			},
		},
		{
			name:  "empty body is omitted",
			title: "Answered",
			sound: SoundDone,
			want:  []string{"/usr/bin/herdr", "notification", "show", "Answered", "--sound", "done"},
		},
		{
			name:  "empty sound defaults to none",
			title: "Heads up",
			want:  []string{"/usr/bin/herdr", "notification", "show", "Heads up", "--sound", "none"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, calls := fakeClient(t, nil)
			if err := c.Notify(context.Background(), tt.title, tt.body, tt.sound); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(*calls))
			}
			if !slices.Equal((*calls)[0], tt.want) {
				t.Errorf("argv mismatch\n got: %q\nwant: %q", (*calls)[0], tt.want)
			}
		})
	}
}

func TestNotifyRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		sound string
	}{
		{name: "blank title", title: "   ", sound: SoundNone},
		{name: "unknown sound", title: "hi", sound: "airhorn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, calls := fakeClient(t, nil)
			if err := c.Notify(context.Background(), tt.title, "", tt.sound); err == nil {
				t.Fatal("want an error")
			}
			if len(*calls) != 0 {
				t.Errorf("herdr was invoked anyway: %q", *calls)
			}
		})
	}
}

func TestPaneTokenArgv(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 100)

	tests := []struct {
		name  string
		call  func(c *Client) error
		want  []string
		wantN int
	}{
		{
			name: "set with ttl",
			call: func(c *Client) error {
				return c.SetPaneToken(context.Background(), "pane-7", "hitl", "? 2", 90*time.Second)
			},
			want: []string{
				"/usr/bin/herdr", "pane", "report-metadata", "pane-7",
				"--source", "herdr-hitl", "--token", "hitl=? 2", "--ttl-ms", "90000",
			},
		},
		{
			name: "zero ttl omits the flag",
			call: func(c *Client) error {
				return c.SetPaneToken(context.Background(), "pane-7", "hitl", "? 1", 0)
			},
			want: []string{
				"/usr/bin/herdr", "pane", "report-metadata", "pane-7",
				"--source", "herdr-hitl", "--token", "hitl=? 1",
			},
		},
		{
			name: "sub-millisecond ttl clamps up to 1",
			call: func(c *Client) error {
				return c.SetPaneToken(context.Background(), "p", "hitl", "?", time.Microsecond)
			},
			want: []string{
				"/usr/bin/herdr", "pane", "report-metadata", "p",
				"--source", "herdr-hitl", "--token", "hitl=?", "--ttl-ms", "1",
			},
		},
		{
			name: "huge ttl clamps down to one day",
			call: func(c *Client) error {
				return c.SetPaneToken(context.Background(), "p", "hitl", "?", 72*time.Hour)
			},
			want: []string{
				"/usr/bin/herdr", "pane", "report-metadata", "p",
				"--source", "herdr-hitl", "--token", "hitl=?", "--ttl-ms", "86400000",
			},
		},
		{
			name: "long value is truncated, not rejected",
			call: func(c *Client) error {
				return c.SetPaneToken(context.Background(), "p", "hitl", long, 0)
			},
			want: []string{
				"/usr/bin/herdr", "pane", "report-metadata", "p",
				"--source", "herdr-hitl", "--token", "hitl=" + strings.Repeat("x", 80),
			},
		},
		{
			name: "clear",
			call: func(c *Client) error {
				return c.ClearPaneToken(context.Background(), "pane-7", "hitl")
			},
			want: []string{
				"/usr/bin/herdr", "pane", "report-metadata", "pane-7",
				"--source", "herdr-hitl", "--clear-token", "hitl",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, calls := fakeClient(t, nil)
			if err := tt.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(*calls))
			}
			if !slices.Equal((*calls)[0], tt.want) {
				t.Errorf("argv mismatch\n got: %q\nwant: %q", (*calls)[0], tt.want)
			}
		})
	}
}

func TestPaneTokenRejectsMissingIdentifiers(t *testing.T) {
	t.Parallel()

	c, calls := fakeClient(t, nil)
	if err := c.SetPaneToken(context.Background(), "", "hitl", "?", 0); err == nil {
		t.Error("SetPaneToken with no pane id: want an error")
	}
	if err := c.SetPaneToken(context.Background(), "p", "", "?", 0); err == nil {
		t.Error("SetPaneToken with no name: want an error")
	}
	if err := c.ClearPaneToken(context.Background(), "p", ""); err == nil {
		t.Error("ClearPaneToken with no name: want an error")
	}
	if len(*calls) != 0 {
		t.Errorf("herdr was invoked anyway: %q", *calls)
	}
}

func TestUnavailableClientIsInert(t *testing.T) {
	t.Parallel()

	var called bool
	c := &Client{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		run: func(context.Context, string, ...string) error { called = true; return nil },
	}
	if c.Available() {
		t.Fatal("Available: want false without a binary")
	}
	for _, err := range []error{
		c.Notify(context.Background(), "t", "b", SoundDone),
		c.SetPaneToken(context.Background(), "p", "hitl", "?", 0),
		c.ClearPaneToken(context.Background(), "p", "hitl"),
	} {
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("got %v, want ErrUnavailable", err)
		}
	}
	if called {
		t.Error("runner ran without a resolved binary")
	}
}

func TestExecErrorIsWrappedWithArgv(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom: unknown flag --token")
	c, _ := fakeClient(t, sentinel)
	err := c.SetPaneToken(context.Background(), "p", "hitl", "?", 0)
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want it to wrap the runner error", err)
	}
	if !strings.Contains(err.Error(), "pane report-metadata") {
		t.Errorf("error %q should name the command", err)
	}
}

func TestExecAppliesDeadline(t *testing.T) {
	t.Parallel()

	var deadline time.Time
	var ok bool
	c, _ := fakeClient(t, nil)
	c.run = func(ctx context.Context, _ string, _ ...string) error {
		deadline, ok = ctx.Deadline()
		return nil
	}
	if err := c.Notify(context.Background(), "t", "", SoundNone); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !ok {
		t.Fatal("runner context has no deadline")
	}
	if d := time.Until(deadline); d <= 0 || d > execTimeout {
		t.Errorf("deadline in %v, want (0, %v]", d, execTimeout)
	}
}

func TestResolveBinPrefersEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "explicit path", env: map[string]string{EnvBinPath: "/opt/herdr/bin/herdr"}, want: "/opt/herdr/bin/herdr"},
		{name: "whitespace is trimmed", env: map[string]string{EnvBinPath: "  /opt/herdr  "}, want: "/opt/herdr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveBin(func(k string) string { return tt.env[k] })
			if got != tt.want {
				t.Errorf("resolveBin = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHelperNoop is the body of the subprocess TestExecRun re-execs. It exists
// so the exec path can be exercised without depending on a system binary.
func TestHelperNoop(t *testing.T) {}

func TestExecRun(t *testing.T) {
	t.Parallel()

	self := os.Args[0]

	if err := execRun(context.Background(), self, "-test.run=TestHelperNoop"); err != nil {
		t.Fatalf("execRun on a succeeding command: %v", err)
	}

	// An unknown flag makes the child fail and print to stderr, which is what
	// the returned error has to carry: "exit status 2" alone is useless.
	err := execRun(context.Background(), self, "-test.not-a-real-flag")
	if err == nil {
		t.Fatal("want an error from a failing command")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("error = %q, want the child's stderr", err)
	}

	if err := execRun(context.Background(), filepath.Join(t.TempDir(), "nope"), "arg"); err == nil {
		t.Error("want an error when the binary does not exist")
	}
}
