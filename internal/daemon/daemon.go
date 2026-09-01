// Package daemon is the resident half of herdr-hitl: one process per machine
// that owns the messenger connections and serves the short-lived CLI over a
// Unix socket or named pipe.
//
// The daemon exists because the messengers demand it. Telegram hands out
// updates from a single destructive per-token queue, so a second getUpdates
// poller steals answers and earns HTTP 409; Discord rate-limits gateway
// IDENTIFY to 1000 per 24 hours and resets the bot token when that is
// exceeded. One long-lived connection is the only safe shape.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
	"github.com/huketo/herdr-hitl/internal/paths"
	"github.com/huketo/herdr-hitl/internal/transport"
)

// TransportFactory builds the messenger transports for a config.
//
// It exists as a seam for two reasons. Tests need a transport that answers
// in-process: the real ones dial Telegram and Discord, and a test suite must
// never do that. And `Run` stays free of transport-specific construction, so
// adding a backend touches one function instead of the daemon lifecycle.
type TransportFactory func(cfg *config.Config, resolver hitl.Resolver, log *slog.Logger) ([]transport.Transport, error)

// Options configures Run.
type Options struct {
	// Config holds the loaded settings. Nil falls back to config.Default.
	Config *config.Config
	// Endpoint is the socket path or pipe name to bind. Empty resolves
	// paths.Socket.
	Endpoint string
	// Version is reported by `herdr-hitl daemon status`.
	Version string
	// Log receives daemon logs. Nil falls back to slog.Default.
	Log *slog.Logger
	// Executable is the binary that was started, recorded in the startup log
	// so a stale daemon left over from an older install is easy to spot.
	Executable string
	// NewTransports overrides transport construction. Nil selects
	// DefaultTransports, which is the only production value.
	NewTransports TransportFactory
}

// idleCheckDivisor and the interval bounds decide how often the idle watchdog
// samples. Sampling at a quarter of the window keeps the overshoot small
// without waking a mostly-idle process every second.
const (
	idleCheckDivisor = 4
	minIdleInterval  = 50 * time.Millisecond
	maxIdleInterval  = 30 * time.Second
)

// Run binds the endpoint, starts the transports, and serves until ctx is done
// or a client asks the daemon to shut down. It returns ipc.ErrAlreadyRunning
// unchanged when another daemon owns the endpoint, so the caller can exit
// quietly instead of fighting over the messenger connection.
func Run(ctx context.Context, opts Options) error {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	cfg := opts.Config
	if cfg == nil {
		cfg = config.Default()
	}

	endpoint := opts.Endpoint
	if endpoint == "" {
		var err error
		if endpoint, err = paths.Socket(); err != nil {
			return err
		}
	}

	// The lock, not the socket, is what makes "one daemon" true. A shutting
	// down daemon unlinks the socket path; without a lifetime lock it can
	// delete the file of a daemon that started in the meantime, leaving two
	// live daemons — and two pollers on one Telegram bot token, which is the
	// exact failure this process exists to prevent.
	lockPath, err := paths.LockFile()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(filepath.Dir(lockPath)); err != nil {
		return err
	}
	releaseLock, locked, err := tryLock(lockPath)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("%w: another daemon holds %s", ipc.ErrAlreadyRunning, lockPath)
	}
	defer releaseLock()

	listener, err := ipc.Listen(endpoint)
	if err != nil {
		// Includes ipc.ErrAlreadyRunning; wrapping it here would only make
		// the caller's errors.Is check read worse.
		return err
	}
	defer func() {
		_ = listener.Close()
		removeEndpoint(endpoint)
	}()

	broker := hitl.NewBroker(log)
	factory := opts.NewTransports
	if factory == nil {
		factory = DefaultTransports
	}
	built, err := factory(cfg, broker, log)
	if err != nil {
		return err
	}

	// runCtx is what the transports, the server, and every in-flight ask hang
	// off. Cancelling it is the whole shutdown sequence: pending questions
	// settle as canceled and the receive loops unwind.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	started, err := startTransports(runCtx, broker, built, log)
	if err != nil {
		return err
	}
	defer closeTransports(started, log)

	svc := newService(cfg, broker, log, endpoint, opts.Version)
	svc.describe = func() []string {
		out := make([]string, 0, len(started))
		for _, tr := range started {
			out = append(out, tr.Describe())
		}
		return out
	}
	defer svc.wait()

	go func() {
		select {
		case <-svc.stop:
			log.Info("shutdown requested")
			stop()
		case <-runCtx.Done():
		}
	}()

	if idle := cfg.Daemon.IdleShutdown.Duration(); idle > 0 {
		go svc.watchIdle(runCtx, idle, stop)
	}

	log.Info("daemon listening",
		"endpoint", endpoint,
		"pid", os.Getpid(),
		"version", opts.Version,
		"executable", opts.Executable,
		"transports", broker.TransportNames())

	if err := ipc.NewServer(svc, log).Serve(runCtx, listener); err != nil {
		return fmt.Errorf("serve %s: %w", endpoint, err)
	}
	log.Info("daemon stopped", "endpoint", endpoint)
	return nil
}

// startTransports brings each transport up and registers the ones that made
// it. A single messenger being down must not take the whole daemon with it:
// the human can still be reached on the other one. Losing all of them is
// fatal, because then nobody can be asked anything.
func startTransports(ctx context.Context, broker *hitl.Broker, built []transport.Transport, log *slog.Logger) ([]transport.Transport, error) {
	started := make([]transport.Transport, 0, len(built))
	var failures []error
	for _, t := range built {
		if err := t.Start(ctx); err != nil {
			log.Error("transport failed to start", "transport", t.Name(), "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", t.Name(), err))
			_ = t.Close()
			continue
		}
		broker.Register(t)
		started = append(started, t)
		log.Info("transport ready", "transport", t.Name(), "detail", t.Describe())
	}
	if len(started) == 0 {
		return nil, fmt.Errorf("%w: no transport could be started: %w",
			hitl.ErrNoTransport, errors.Join(failures...))
	}
	return started, nil
}

// closeTransports tears the messenger connections down.
func closeTransports(started []transport.Transport, log *slog.Logger) {
	for _, t := range started {
		if err := t.Close(); err != nil {
			log.Warn("transport close failed", "transport", t.Name(), "error", err)
		}
	}
}

// idleInterval picks the watchdog sampling period for an idle window.
func idleInterval(idle time.Duration) time.Duration {
	interval := idle / idleCheckDivisor
	if interval < minIdleInterval {
		return minIdleInterval
	}
	if interval > maxIdleInterval {
		return maxIdleInterval
	}
	return interval
}
