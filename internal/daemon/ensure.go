package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/huketo/herdr-hitl/internal/ipc"
	"github.com/huketo/herdr-hitl/internal/paths"
)

// spawnWait bounds how long a CLI waits for a freshly spawned daemon to accept
// connections. Discord's gateway handshake dominates it, so the budget is
// generous; a CLI that gives up early would post nothing at all.
const spawnWait = 15 * time.Second

// Probe backoff for the startup wait: quick first checks so a warm daemon
// costs milliseconds, then slower ones so a cold Discord connect does not spin
// the CPU.
const (
	probeInitialDelay = 25 * time.Millisecond
	probeMaxDelay     = 250 * time.Millisecond
	probeGrowth       = 3
	probeShrink       = 2
)

// EnsureRunning makes sure a daemon is listening on endpoint, spawning one
// detached if it is not, and returns once the daemon answers.
//
// executable is the binary to re-exec (`<executable> serve`); empty resolves
// os.Executable. endpoint empty resolves paths.Socket.
func EnsureRunning(ctx context.Context, endpoint, executable string, log *slog.Logger) error {
	return ensureRunning(ctx, endpoint, executable, log, spawnWait)
}

// ensureRunning is EnsureRunning with an injectable startup budget so tests do
// not have to wait out the production one.
func ensureRunning(ctx context.Context, endpoint, executable string, log *slog.Logger, wait time.Duration) error {
	if log == nil {
		log = slog.Default()
	}
	if endpoint == "" {
		var err error
		if endpoint, err = paths.Socket(); err != nil {
			return err
		}
	}
	if ipc.Probe(ctx, endpoint) {
		return nil
	}
	if executable == "" {
		var err error
		if executable, err = os.Executable(); err != nil {
			return fmt.Errorf("locate herdr-hitl binary: %w", err)
		}
	}

	// The lock is held for the spawn *and* the wait. Two concurrent asks
	// therefore produce one daemon: the loser skips straight to waiting
	// instead of racing for the messenger connection.
	release, locked, lockErr := lockForSpawn(log)
	if locked {
		defer release()
	}
	// Everything the child writes past this point is its own. Blaming a
	// week-old failure on a daemon that is starting now would be worse than
	// saying nothing.
	baseline := LogSize()

	switch {
	// A broken lock is not a reason to give up: ipc.Listen is the real
	// arbiter, so a redundant spawn costs one process that exits immediately.
	case locked, lockErr != nil:
		if err := spawn(executable, log); err != nil {
			return err
		}
	default:
		log.Debug("another process is starting the daemon; waiting")
	}

	return waitForDaemon(ctx, endpoint, wait, baseline)
}

// lockForSpawn takes the single-daemon lock. A lock that cannot be taken for
// any reason other than contention is reported so the caller can spawn anyway:
// ipc.Listen is the real arbiter, the lock only saves a wasted process.
func lockForSpawn(log *slog.Logger) (release func(), locked bool, err error) {
	lockPath, err := paths.SpawnLockFile()
	if err != nil {
		return nil, false, err
	}
	if err := paths.EnsureDir(filepath.Dir(lockPath)); err != nil {
		return nil, false, err
	}
	release, locked, err = tryLock(lockPath)
	if err != nil {
		log.Debug("daemon lock failed", "path", lockPath, "error", err)
	}
	return release, locked, err
}

// spawn starts `<executable> serve` as a detached background process with its
// output appended to the daemon log.
func spawn(executable string, log *slog.Logger) error {
	logPath, err := paths.LogFile()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(filepath.Dir(logPath)); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	// Background, not the caller's context: the daemon must outlive the CLI
	// invocation that started it, so its lifetime is never tied to this ask.
	cmd := exec.CommandContext(context.Background(), executable, "serve")
	cmd.Cancel = func() error { return nil }
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon %s serve: %w", executable, err)
	}
	pid := cmd.Process.Pid

	// Release drops the child: the CLI exits as soon as its ask is answered
	// and must not be the process that has to reap the daemon.
	if err := cmd.Process.Release(); err != nil {
		log.Debug("release daemon process", "pid", pid, "error", err)
	}
	log.Debug("daemon spawned", "pid", pid, "log", logPath)
	return nil
}

// waitForDaemon polls the endpoint until a daemon answers, the child reports
// a fatal error, or the budget runs out.
//
// Watching the log as well as the socket is what turns "daemon did not start
// within 15s" into the actual reason, and returns it in a moment rather than
// after the full budget. A misconfigured transport is the common first-run
// failure and it is diagnosed in the child, not here.
func waitForDaemon(ctx context.Context, endpoint string, wait time.Duration, baseline int64) error {
	deadline := time.Now().Add(wait)
	delay := probeInitialDelay

	for {
		if ipc.Probe(ctx, endpoint) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if reason := LastFailureSince(baseline); reason != "" {
			return fmt.Errorf("the daemon exited during startup: %s", reason)
		}
		if time.Now().After(deadline) {
			break
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < probeMaxDelay {
			delay = delay * probeGrowth / probeShrink
		}
	}

	logPath, err := paths.LogFile()
	if err != nil {
		return fmt.Errorf("daemon did not start within %s on %s", wait, endpoint)
	}
	return fmt.Errorf("daemon did not start within %s on %s; see %s", wait, endpoint, logPath)
}
