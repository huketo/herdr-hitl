// Package herdrctl calls back into the Herdr CLI so a pending question is
// visible inside the terminal the agent runs in: a toast when the question is
// posted, and a `$hitl` token on the asking pane while it is outstanding.
//
// Every call is best-effort. Herdr may not be installed, may not be running,
// and its CLI surface may lag the plugin. A human decision must never fail
// because a toast did not render, so callers log failures and carry on.
package herdrctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// EnvBinPath names the Herdr binary a plugin should call back into. Herdr
// exports it to every plugin process; `herdr` on PATH is the fallback for
// running the CLI outside a Herdr session.
const EnvBinPath = "HERDR_BIN_PATH"

// binName is the PATH fallback for EnvBinPath.
const binName = "herdr"

// Source is the value passed as `--source`. Herdr requires it to match
// [A-Za-z0-9:._-]{1,80} and uses it to scope the metadata a plugin may clear.
const Source = "herdr-hitl"

// Notification sounds accepted by `herdr notification show --sound`.
const (
	// SoundNone shows a silent toast.
	SoundNone = "none"
	// SoundDone marks work that finished.
	SoundDone = "done"
	// SoundRequest marks work that is waiting on the human.
	SoundRequest = "request"
)

// execTimeout bounds a single Herdr invocation. The Herdr CLI talks to a local
// daemon, so anything slower than this is a hang, and a hang here would stall
// an agent that is only trying to ask a question.
const execTimeout = 5 * time.Second

// maxTokenValueRunes is Herdr's cap on a metadata token value. Longer values
// are truncated rather than rejected: a clipped sidebar label is a better
// outcome than a failed ask.
const maxTokenValueRunes = 80

// ttlBounds are Herdr's accepted --ttl-ms range.
const (
	minTTLMillis = 1
	maxTTLMillis = 86_400_000
)

// ErrUnavailable means no Herdr binary could be resolved, so the callbacks are
// no-ops. Callers treat it as "not running under Herdr", not as a failure.
var ErrUnavailable = errors.New("herdrctl: herdr binary not found")

// runner executes a Herdr command. It is the seam the tests replace, which
// keeps argv construction testable without a Herdr installation.
type runner func(ctx context.Context, name string, args ...string) error

// Client invokes the Herdr CLI.
type Client struct {
	log *slog.Logger
	// bin is the resolved binary path, or "" when Herdr is unavailable.
	bin string
	run runner
}

// New resolves the Herdr binary once, at daemon startup, and returns a client
// for it. A nil logger is replaced by slog.Default.
func New(log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	bin := resolveBin(os.Getenv)
	if bin == "" {
		log.Debug("herdr cli not found; notifications and pane tokens are disabled")
	}
	return &Client{log: log, bin: bin, run: execRun}
}

// Available reports whether a Herdr binary was found. `doctor` prints it, and
// the daemon skips the callbacks entirely when it is false.
func (c *Client) Available() bool { return c != nil && c.bin != "" }

// Notify raises a Herdr toast. sound must be one of SoundNone, SoundDone, or
// SoundRequest.
func (c *Client) Notify(ctx context.Context, title, body, sound string) error {
	if !c.Available() {
		return ErrUnavailable
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("herdrctl: notification needs a title")
	}
	switch sound {
	case "":
		sound = SoundNone
	case SoundNone, SoundDone, SoundRequest:
	default:
		return fmt.Errorf("herdrctl: unknown notification sound %q", sound)
	}

	args := []string{"notification", "show", title, "--sound", sound}
	if body != "" {
		args = append(args, "--body", body)
	}
	return c.exec(ctx, args...)
}

// SetPaneToken publishes a metadata token on a Herdr pane, which renders it in
// the pane's sidebar entry. ttl <= 0 publishes the token without an expiry.
func (c *Client) SetPaneToken(ctx context.Context, paneID, name, value string, ttl time.Duration) error {
	if !c.Available() {
		return ErrUnavailable
	}
	if paneID == "" || name == "" {
		return errors.New("herdrctl: pane token needs a pane id and a name")
	}

	args := []string{
		"pane", "report-metadata", paneID, "--source", Source,
		"--token", name + "=" + truncateRunes(value, maxTokenValueRunes),
	}
	if ttl > 0 {
		args = append(args, "--ttl-ms", strconv.FormatInt(clampMillis(ttl), 10))
	}
	return c.exec(ctx, args...)
}

// ClearPaneToken removes a token this plugin published.
func (c *Client) ClearPaneToken(ctx context.Context, paneID, name string) error {
	if !c.Available() {
		return ErrUnavailable
	}
	if paneID == "" || name == "" {
		return errors.New("herdrctl: clearing a pane token needs a pane id and a name")
	}
	return c.exec(ctx, "pane", "report-metadata", paneID,
		"--source", Source, "--clear-token", name)
}

// exec bounds the call and hands it to the runner. The deadline is applied
// here, not in the runner, so injected runners inherit it too.
func (c *Client) exec(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	if err := c.run(ctx, c.bin, args...); err != nil {
		return fmt.Errorf("herdr %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// execRun is the production runner: it captures stderr so the returned error
// says what Herdr complained about instead of just "exit status 1".
func execRun(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, firstLine(msg))
		}
		return err
	}
	return nil
}

// resolveBin prefers the binary Herdr told us about; otherwise it looks for
// `herdr` on PATH. An explicit HERDR_BIN_PATH is trusted as given so that a
// binary outside PATH still works.
func resolveBin(get func(string) string) string {
	if bin := strings.TrimSpace(get(EnvBinPath)); bin != "" {
		return bin
	}
	bin, err := exec.LookPath(binName)
	if err != nil {
		return ""
	}
	return bin
}

// clampMillis maps a duration onto Herdr's accepted --ttl-ms window.
func clampMillis(d time.Duration) int64 {
	ms := d.Milliseconds()
	if ms < minTTLMillis {
		return minTTLMillis
	}
	if ms > maxTTLMillis {
		return maxTTLMillis
	}
	return ms
}

// truncateRunes clips s to at most n runes without splitting one.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// firstLine keeps an error message to one line so it composes with slog and
// wrapped errors.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
