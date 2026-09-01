package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/daemon"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

func newServeCommand(g *globals) *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the resident daemon in the foreground",
		Long: "Hold the messenger connections and serve CLI clients over the local\n" +
			"socket. Only one daemon may own a bot token: Telegram deletes updates\n" +
			"from a single destructive queue, and a second poller steals answers.\n" +
			"If another daemon already owns the endpoint, this exits 0 quietly.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --foreground exists for symmetry with `daemon start`; the
			// daemon never backgrounds itself.
			return runServe(cmd, g)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", true, "stay in the foreground (the only supported mode)")
	return cmd
}

func runServe(cmd *cobra.Command, g *globals) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	endpoint, err := g.endpoint()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return failf("locate executable: %w", err)
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	opts := daemon.Options{
		Config:     cfg,
		Endpoint:   endpoint,
		Version:    g.info.Version,
		Log:        serveLogger(g, cmd.ErrOrStderr(), cfg.Daemon.LogLevel),
		Executable: exe,
	}
	switch err := daemon.Run(ctx, opts); {
	case err == nil, errors.Is(err, context.Canceled):
		return nil
	case errors.Is(err, ipc.ErrAlreadyRunning):
		fmt.Fprintf(cmd.ErrOrStderr(), "herdr-hitl: another daemon already owns %s\n", endpoint)
		return nil
	default:
		return failf("serve: %w", err)
	}
}

// serveLogger honours the configured log level, which matters more for the
// resident process than for a one-shot command.
func serveLogger(g *globals, w io.Writer, level string) *slog.Logger {
	if g.verbose || g.quiet || level == "" {
		return g.logger(w)
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToLower(level))); err != nil {
		return g.logger(w)
	}
	g.log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
	return g.log
}

func newDaemonCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Control the resident daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	start := &cobra.Command{
		Use:          "start",
		Short:        "Start the daemon in the background if it is not running",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemonStart(cmd, g)
		},
	}

	stop := &cobra.Command{
		Use:          "stop",
		Short:        "Stop the daemon",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemonStop(cmd, g)
		},
	}

	var statusFormat string
	status := &cobra.Command{
		Use:          "status",
		Short:        "Report daemon health",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemonStatus(cmd, g, statusFormat)
		},
	}
	status.Flags().StringVarP(&statusFormat, "format", "o", formatText, "output format: text | json")

	restart := &cobra.Command{
		Use:          "restart",
		Short:        "Stop the daemon and start a fresh one",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := runDaemonStop(cmd, g); err != nil {
				return err
			}
			return runDaemonStart(cmd, g)
		},
	}

	cmd.AddCommand(start, stop, status, restart)
	return cmd
}

func runDaemonStart(cmd *cobra.Command, g *globals) error {
	endpoint, err := g.endpoint()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return failf("locate executable: %w", err)
	}
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	if err := daemon.EnsureRunning(ctx, endpoint, exe, g.logger(cmd.ErrOrStderr())); err != nil {
		return failf("start daemon: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "daemon running on %s\n", endpoint)
	return nil
}

func runDaemonStop(cmd *cobra.Command, g *globals) error {
	endpoint, err := g.endpoint()
	if err != nil {
		return err
	}
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	if !ipc.Probe(ctx, endpoint) {
		fmt.Fprintln(cmd.OutOrStdout(), "daemon is not running")
		return nil
	}
	if _, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpShutdown}); err != nil {
		// A daemon that closes the socket while shutting down is a success,
		// not a failure worth an exit code.
		if errors.Is(err, ipc.ErrDaemonUnavailable) {
			fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return nil
		}
		return failf("stop daemon: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
	return nil
}

func runDaemonStatus(cmd *cobra.Command, g *globals, format string) error {
	if format != formatText && format != formatJSON {
		return usagef("--format %q: expected %q or %q", format, formatText, formatJSON)
	}
	endpoint, err := g.endpoint()
	if err != nil {
		return err
	}
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	resp, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpStatus})
	if err != nil {
		if errors.Is(err, ipc.ErrDaemonUnavailable) {
			if format == formatJSON {
				if err := writeJSON(cmd.OutOrStdout(), map[string]any{
					"running": false, "socket": endpoint,
				}); err != nil {
					return err
				}
				return silentCode(ExitError, errors.New("daemon is not running"))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "not running (%s)\n", endpoint)
			return silentCode(ExitError, errors.New("daemon is not running"))
		}
		return failf("status: %w", err)
	}
	st := resp.Status
	if st == nil {
		return failf("daemon returned no status")
	}
	if format == formatJSON {
		return writeJSON(cmd.OutOrStdout(), st)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "running   pid %d\n", st.PID)
	fmt.Fprintf(out, "version   %s\n", st.Version)
	fmt.Fprintf(out, "socket    %s\n", st.Socket)
	fmt.Fprintf(out, "transports %s\n", listOr(st.Transports, "none"))
	fmt.Fprintf(out, "pending   %d\n", st.Pending)
	fmt.Fprintf(out, "uptime    %s (since %s)\n", st.Uptime, st.StartedAt)
	return nil
}

func listOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}
