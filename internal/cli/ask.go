package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/daemon"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

// askOptions holds the flags shared by `ask` and `notify`.
type askOptions struct {
	title       string
	message     string
	messageFile string
	choices     []string
	primary     []string
	danger      []string
	free        bool
	attach      []string
	timeout     time.Duration
	transports  []string
	agent       string
	deflt       string
	format      string
}

// bindCommon registers the flags `ask` and `notify` have in common.
func (o *askOptions) bindCommon(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVarP(&o.title, "title", "t", "", "one-line summary")
	f.StringVarP(&o.message, "message", "m", "", `question body, Markdown; "-" reads stdin`)
	f.StringVar(&o.messageFile, "message-file", "", "read the body from a file")
	f.StringSliceVarP(&o.attach, "attach", "a", nil, "path to an image or document (repeatable)")
	f.StringSliceVar(&o.transports, "transport", nil, "telegram | discord (default: config)")
	f.StringVar(&o.agent, "agent", "", `label shown to the human (default: $HITL_AGENT, else "agent")`)
}

func newAskCommand(g *globals) *cobra.Command {
	o := &askOptions{}
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "Ask a human a question and block until they answer",
		Long: "Post a question to the configured messengers and wait for a reply.\n\n" +
			"With -o text the answer text is the only thing written to stdout, so\n" +
			"`answer=$(herdr-hitl ask -t 'Deploy?' -c yes -c no)` works. Diagnostics go\n" +
			"to stderr. Exit codes: 0 answered, 1 error, 2 usage, 3 timeout, 4 canceled.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAsk(cmd, g, o)
		},
	}
	o.bindCommon(cmd)
	f := cmd.Flags()
	f.StringSliceVarP(&o.choices, "choice", "c", nil, `predefined answer, "id=Label" or "Label" (repeatable)`)
	f.StringSliceVar(&o.primary, "primary", nil, "choice ids rendered as the primary button")
	f.StringSliceVar(&o.danger, "danger", nil, "choice ids rendered as the destructive button")
	f.BoolVar(&o.free, "free", true, "allow a free-text answer (--free=false forces a choice)")
	f.DurationVar(&o.timeout, "timeout", 0, "answer deadline; 0 waits forever (default: config)")
	f.StringVar(&o.deflt, "default", "", "text to print if the deadline passes, instead of failing")
	f.StringVarP(&o.format, "format", "o", formatText, "output format: text | json")
	return cmd
}

func newNotifyCommand(g *globals) *cobra.Command {
	o := &askOptions{}
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Send a message that expects no answer",
		Long: "Post a one-way message to the configured messengers and return\n" +
			"immediately. Nothing is written to stdout on success.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNotify(cmd, g, o)
		},
	}
	o.bindCommon(cmd)
	return cmd
}

// build turns the flags into wire parameters, failing fast on anything the
// daemon would only discover at upload time.
func (o *askOptions) build(cmd *cobra.Command, cfg *config.Config, wantAnswer bool) (*ipc.AskParams, error) {
	stdin := cmd.InOrStdin()
	body, err := resolveBody(o.message, o.messageFile, o.title, stdin, isTerminal(stdin))
	if err != nil {
		return nil, err
	}
	choices, err := parseChoices(o.choices, o.primary, o.danger)
	if err != nil {
		return nil, err
	}
	attachments, err := resolveAttachments(o.attach)
	if err != nil {
		return nil, err
	}
	transports, err := resolveTransports(o.transports, cfg)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout.Duration()
	if cmd.Flags().Changed("timeout") {
		timeout = o.timeout
	}
	if timeout < 0 {
		return nil, usagef("--timeout must not be negative")
	}
	free := o.free
	if !wantAnswer {
		// A notification has no reply surface at all.
		free = false
		choices = nil
		timeout = 0
	}

	params := &ipc.AskParams{
		Title:         strings.TrimSpace(o.title),
		Body:          body,
		Choices:       choices,
		AllowFreeText: free,
		Attachments:   attachments,
		Timeout:       ipc.Duration(timeout),
		Transports:    transports,
		Origin:        detectOrigin(o.agent),
	}

	if wantAnswer {
		// Validate against the domain rules before paying for a daemon
		// round trip; the error text is identical either way but arrives
		// instantly and without posting anything.
		probe := &hitl.Request{
			Title:         params.Title,
			Body:          params.Body,
			Choices:       params.Choices,
			AllowFreeText: params.AllowFreeText,
			Timeout:       timeout,
		}
		if err := probe.Validate(); err != nil {
			return nil, usagef("%w", err)
		}
	}
	return params, nil
}

// resolveTransports validates --transport, falling back to the config default
// so that the `transports` config key is honoured even though the daemon holds
// its own copy of the configuration.
func resolveTransports(names []string, cfg *config.Config) ([]string, error) {
	if len(names) == 0 {
		return cfg.DefaultTransports(), nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case "":
			continue
		case config.TransportTelegram, config.TransportDiscord:
			out = append(out, name)
		case "all":
			return cfg.EnabledTransports(), nil
		default:
			return nil, usagef("--transport %q: expected %q or %q", name,
				config.TransportTelegram, config.TransportDiscord)
		}
	}
	return out, nil
}

func runAsk(cmd *cobra.Command, g *globals, o *askOptions) error {
	if o.format != formatText && o.format != formatJSON {
		return usagef("--format %q: expected %q or %q", o.format, formatText, formatJSON)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	params, err := o.build(cmd, cfg, true)
	if err != nil {
		return err
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	client, endpoint, err := connect(ctx, g, cmd.ErrOrStderr(), true)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	g.logger(cmd.ErrOrStderr()).Debug("asking", "endpoint", endpoint, "timeout", params.Timeout.String())

	resp, callErr := client.Do(ctx, &ipc.Request{Op: ipc.OpAsk, Ask: params})

	var answer *hitl.Answer
	if resp != nil {
		answer = resp.Answer
	}
	outcome := callErr
	if outcome == nil && answer != nil {
		// The broker reports a deadline or a withdrawal as a non-answered
		// Answer rather than a transport failure.
		outcome = answer.Err()
	}

	switch {
	case outcome == nil && answer == nil:
		return failf("daemon returned no answer")

	case outcome == nil:
		return printAnswer(cmd.OutOrStdout(), o.format, answer)

	case errors.Is(outcome, hitl.ErrTimeout):
		if cmd.Flags().Changed("default") {
			return printAnswer(cmd.OutOrStdout(), o.format, timeoutAnswer(answer, o.deflt))
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "herdr-hitl: no answer within %s\n", params.Timeout)
		return silentCode(ExitTimeout, outcome)

	case errors.Is(outcome, hitl.ErrCanceled), errors.Is(outcome, context.Canceled):
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "herdr-hitl: question canceled")
		return silentCode(ExitCanceled, outcome)

	default:
		return failf("ask: %w", explainDaemonLoss(outcome))
	}
}

// timeoutAnswer describes a deadline that --default papered over: the status
// stays honest, the text is what the caller asked to fall back to.
func timeoutAnswer(answer *hitl.Answer, text string) *hitl.Answer {
	out := &hitl.Answer{Status: hitl.StatusTimeout, Reason: "default used"}
	if answer != nil {
		out.RequestID = answer.RequestID
		if answer.Reason != "" {
			out.Reason = answer.Reason + "; default used"
		}
	}
	out.Text = text
	return out
}

// printAnswer writes the answer in the requested format. In text mode stdout
// carries the answer and nothing else, so command substitution works.
func printAnswer(w io.Writer, format string, answer *hitl.Answer) error {
	if format == formatJSON {
		return writeJSON(w, answer)
	}
	_, err := fmt.Fprintln(w, answer.Text)
	return err
}

func runNotify(cmd *cobra.Command, g *globals, o *askOptions) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	params, err := o.build(cmd, cfg, false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(params.Title) == "" && strings.TrimSpace(params.Body) == "" {
		return usagef("notify: nothing to send, pass --title or --message")
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	client, _, err := connect(ctx, g, cmd.ErrOrStderr(), true)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Do(ctx, &ipc.Request{Op: ipc.OpNotify, Notify: params}); err != nil {
		return failf("notify: %w", explainDaemonLoss(err))
	}
	return nil
}

// explainDaemonLoss adds what the daemon said before it died.
//
// The daemon binds the endpoint before it starts its transports, so a daemon
// that cannot reach Telegram or Discord still answers the probe and then
// exits mid-request. Reported bare, that reaches the user as
// "read: connection reset by peer", which is true and useless. The daemon
// wrote the real reason to its log one moment earlier; this puts the two
// together.
func explainDaemonLoss(err error) error {
	if err == nil {
		return nil
	}
	if !isConnectionLoss(err) {
		return err
	}
	if last := daemon.LastFailure(); last != "" {
		return fmt.Errorf("the daemon exited while handling the request: %s", last)
	}
	return fmt.Errorf("%w (the daemon exited; see the log named by `herdr-hitl doctor`)", err)
}

// isConnectionLoss reports whether err means the daemon went away rather than
// refused the request.
func isConnectionLoss(err error) bool {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return true
	case errors.Is(err, ipc.ErrDaemonUnavailable):
		return true
	default:
		// A daemon that dies mid-write surfaces as a *net.OpError whose
		// wrapped errno varies by platform; the wire error is never an
		// ipc.Error, so anything that is not one is a transport-level loss.
		var wire *ipc.Error
		return !errors.As(err, &wire) && errors.Is(err, net.ErrClosed)
	}
}

// connect dials the daemon, optionally starting it first. Commands that only
// inspect existing state pass autostart=false: spawning a daemon to be told
// nothing is pending would be absurd.
func connect(ctx context.Context, g *globals, stderr io.Writer, autostart bool) (*ipc.Client, string, error) {
	endpoint, err := g.endpoint()
	if err != nil {
		return nil, "", err
	}
	if autostart {
		exe, err := os.Executable()
		if err != nil {
			return nil, "", failf("locate executable: %w", err)
		}
		if err := daemon.EnsureRunning(ctx, endpoint, exe, g.logger(stderr)); err != nil {
			return nil, "", failf("start daemon: %w", err)
		}
	}
	client, err := ipc.Dial(ctx, endpoint)
	if err != nil {
		if errors.Is(err, ipc.ErrDaemonUnavailable) && !autostart {
			return nil, "", failf("daemon is not running (start it with `herdr-hitl daemon start`)")
		}
		return nil, "", failf("connect to daemon: %w", err)
	}
	return client, endpoint, nil
}

// loadConfig reports a bad configuration as a usage error: the fix is always
// in the operator's config file or environment, never at runtime.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, usagef("%w", err)
	}
	return cfg, nil
}

// writeJSON emits an indented document with a trailing newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return failf("encode json: %w", err)
	}
	return nil
}
