// Package cli builds the herdr-hitl command tree.
//
// The binary is both the agent-facing client and the resident daemon, so the
// same command tree covers `ask` (blocking, machine-readable) and `serve`
// (long-lived). Exit codes are part of the contract an agent scripts against:
// 0 answered, 1 error, 2 usage, 3 timeout, 4 canceled, 5 terminal channel.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/paths"
)

// Exit codes. An agent branches on these, so they never move.
const (
	// ExitOK means the question was answered, or the command succeeded.
	ExitOK = 0
	// ExitError is any runtime failure.
	ExitError = 1
	// ExitUsage is a bad invocation: unknown flag, missing argument.
	ExitUsage = 2
	// ExitTimeout means nobody answered before the deadline.
	ExitTimeout = 3
	// ExitCanceled means the question was withdrawn or declined.
	ExitCanceled = 4
	// ExitTerminal means nothing was delivered because the resolved channel
	// is the terminal: the human is at the agent's own interface, so the
	// agent must ask there. It is not a failure and never an approval.
	ExitTerminal = 5
)

// Output formats accepted by -o/--format.
const (
	formatText = "text"
	formatJSON = "json"
)

// BuildInfo carries the values stamped in at link time.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// exitError attaches an exit code to a failure. Commands return it whenever
// the default mapping (1) is wrong.
type exitError struct {
	code int
	err  error
	// silent suppresses the top-level "herdr-hitl: ..." line because the
	// command already printed a better diagnostic of its own.
	silent bool
}

func (e *exitError) Error() string { return e.err.Error() }

// Unwrap keeps errors.Is working through the exit-code wrapper.
func (e *exitError) Unwrap() error { return e.err }

// usagef reports a bad invocation (exit 2).
func usagef(format string, args ...any) error {
	return &exitError{code: ExitUsage, err: fmt.Errorf(format, args...)}
}

// failf reports a runtime failure (exit 1).
func failf(format string, args ...any) error {
	return &exitError{code: ExitError, err: fmt.Errorf(format, args...)}
}

// withCode tags an existing error with an exit code.
func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: code, err: err}
}

// silentCode tags an error whose diagnosis the command already printed.
func silentCode(code int, err error) error {
	return &exitError{code: code, err: err, silent: true}
}

// silenced reports whether the top-level error line would be a duplicate.
func silenced(err error) bool {
	var ee *exitError
	return errors.As(err, &ee) && ee.silent
}

// exitCode maps a command failure onto the documented exit codes. The domain
// sentinels win over the wrapper code because ipc.Error unwraps to them and a
// timeout must stay a timeout however deep it is wrapped.
func exitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, hitl.ErrTimeout):
		return ExitTimeout
	case errors.Is(err, hitl.ErrCanceled), errors.Is(err, context.Canceled):
		return ExitCanceled
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return ExitError
}

// globals holds the root command's persistent flags plus the state every
// subcommand needs.
type globals struct {
	info BuildInfo

	configDir string
	socket    string
	verbose   bool
	quiet     bool

	// ran records whether execution reached a command body. Cobra reports
	// flag-parse and argument-validation failures before that point, and
	// those must exit 2 without string-matching cobra's messages.
	ran bool

	log *slog.Logger
}

// endpoint resolves the daemon socket, honouring --socket.
func (g *globals) endpoint() (string, error) {
	if g.socket != "" {
		return g.socket, nil
	}
	endpoint, err := paths.Socket()
	if err != nil {
		return "", failf("resolve socket: %w", err)
	}
	return endpoint, nil
}

// logger returns the process logger, writing diagnostics to stderr so that
// stdout stays reserved for the answer.
func (g *globals) logger(w io.Writer) *slog.Logger {
	if g.log != nil {
		return g.log
	}
	level := slog.LevelWarn
	switch {
	case g.verbose:
		level = slog.LevelDebug
	case g.quiet:
		level = slog.LevelError
	}
	g.log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
	return g.log
}

// signalContext cancels ctx on SIGINT/SIGTERM. A cancelled context closes the
// daemon connection, which is how a Ctrl-C'd agent withdraws its question.
func signalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}

// NewRootCommand assembles the whole command tree.
func NewRootCommand(info BuildInfo) *cobra.Command {
	root, _ := newRootCommand(info)
	return root
}

// newRootCommand also hands back the flag state, which Main needs after
// Execute to tell a rejected invocation apart from a failed command.
func newRootCommand(info BuildInfo) (*cobra.Command, *globals) {
	// Resolved in one place so version, doctor, and daemon status all agree.
	g := &globals{info: info.Resolve()}

	root := &cobra.Command{
		Use:   "herdr-hitl",
		Short: "Ask a human a question from a coding agent, over Telegram or Discord",
		Long: "herdr-hitl blocks an agent on a human decision.\n\n" +
			"`ask` posts a question to a messenger and waits for the reply, printing the\n" +
			"answer on stdout. A resident daemon owns the messenger connections; the CLI\n" +
			"is a thin client over a local socket and starts the daemon on demand.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{HiddenDefaultCmd: true},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			g.ran = true
			if g.configDir != "" {
				if err := os.Setenv(paths.EnvConfigDir, g.configDir); err != nil {
					return failf("set %s: %w", paths.EnvConfigDir, err)
				}
			}
			g.logger(cmd.ErrOrStderr())
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.configDir, "config-dir", "", "override the configuration directory")
	pf.StringVar(&g.socket, "socket", "", "override the daemon endpoint")
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "log debug detail to stderr")
	pf.BoolVarP(&g.quiet, "quiet", "q", false, "suppress diagnostics on stderr")

	// Cobra reports flag problems through this hook; they are usage errors.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return withCode(ExitUsage, err)
	})

	root.AddCommand(
		newAskCommand(g),
		newNotifyCommand(g),
		newPendingCommand(g),
		newAnswerCommand(g),
		newCancelCommand(g),
		newServeCommand(g),
		newDaemonCommand(g),
		newDoctorCommand(g),
		newConfigCommand(g),
		newInstallCLICommand(g),
		newChannelCommand(g),
		newAwayCommand(g),
		newHereCommand(g),
		newVersionCommand(g),
	)
	return root, g
}

// Main runs the command tree and returns the process exit code.
func Main(info BuildInfo) int {
	root, g := newRootCommand(info)

	err := root.Execute()
	if err == nil {
		return ExitOK
	}

	code := exitCode(err)
	// Cobra rejected the invocation before any command body ran: that is a
	// usage error even when the message carries no code of its own.
	if !g.ran {
		code = ExitUsage
		defer fmt.Fprintf(os.Stderr, "Run '%s --help' for usage.\n", root.Name())
	}
	if !g.quiet && !silenced(err) {
		fmt.Fprintln(os.Stderr, "herdr-hitl: "+strings.TrimSpace(err.Error()))
	}
	return code
}
