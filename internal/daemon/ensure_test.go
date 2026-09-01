package daemon

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/paths"
	"github.com/huketo/herdr-hitl/internal/transport"
)

func TestEnsureRunningSkipsSpawnWhenDaemonAnswers(t *testing.T) {
	endpoint := stateEndpoint(t)

	factory := func(_ *config.Config, resolver hitl.Resolver, _ *slog.Logger) ([]transport.Transport, error) {
		return []transport.Transport{newFakeTransport("fake", resolver)}, nil
	}
	stop := runningDaemon(t, config.Default(), endpoint, factory)
	defer stop()

	// The bogus executable proves nothing was spawned: a spawn attempt would
	// fail loudly.
	if err := ensureRunning(context.Background(), endpoint, filepath.Join(t.TempDir(), "nope"),
		discardLogger(), time.Second); err != nil {
		t.Fatalf("EnsureRunning against a live daemon = %v, want nil", err)
	}
}

func TestEnsureRunningReportsSpawnFailure(t *testing.T) {
	endpoint := stateEndpoint(t)

	err := ensureRunning(context.Background(), endpoint,
		filepath.Join(t.TempDir(), "missing-herdr-hitl"), discardLogger(), 200*time.Millisecond)
	if err == nil {
		t.Fatal("want an error when the daemon binary cannot be started")
	}
	if !strings.Contains(err.Error(), "start daemon") {
		t.Errorf("error = %v, want it to name the failed spawn", err)
	}
}

func TestEnsureRunningWaitsInsteadOfSpawningWhenLocked(t *testing.T) {
	endpoint := stateEndpoint(t)

	lockPath, err := paths.LockFile()
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	if err := paths.EnsureDir(filepath.Dir(lockPath)); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	release, locked, err := tryLock(lockPath)
	if err != nil || !locked {
		t.Fatalf("tryLock = (%v, %v), want the lock", locked, err)
	}
	defer release()

	// Another process holds the lock, so this call must not spawn a second
	// daemon; it waits, then reports the timeout and points at the log.
	err = ensureRunning(context.Background(), endpoint,
		filepath.Join(t.TempDir(), "missing-herdr-hitl"), discardLogger(), 150*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if strings.Contains(err.Error(), "start daemon") {
		t.Errorf("error = %v, want the wait to time out rather than spawn", err)
	}
	logPath, logErr := paths.LogFile()
	if logErr != nil {
		t.Fatalf("log path: %v", logErr)
	}
	if !strings.Contains(err.Error(), logPath) {
		t.Errorf("error = %v, want it to point at %s", err, logPath)
	}
}

func TestTryLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")

	release, locked, err := tryLock(path)
	if err != nil || !locked {
		t.Fatalf("first tryLock = (%v, %v), want the lock", locked, err)
	}

	if _, locked, err := tryLock(path); err != nil || locked {
		t.Fatalf("second tryLock = (%v, %v), want contention", locked, err)
	}

	release()
	release2, locked, err := tryLock(path)
	if err != nil || !locked {
		t.Fatalf("tryLock after release = (%v, %v), want the lock", locked, err)
	}
	release2()
}

func TestWaitForDaemonHonoursContext(t *testing.T) {
	endpoint := stateEndpoint(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := waitForDaemon(ctx, endpoint, time.Minute); err == nil {
		t.Fatal("want the canceled context to be reported")
	}
}

func TestDefaultTransportsWithoutConfiguration(t *testing.T) {
	t.Parallel()

	_, err := DefaultTransports(config.Default(), hitl.NewBroker(discardLogger()), discardLogger())
	if err == nil {
		t.Fatal("want an error when no messenger is configured")
	}
	if !errors.Is(err, hitl.ErrNoTransport) {
		t.Errorf("error = %v, want ErrNoTransport", err)
	}
	for _, want := range []string{"herdr-hitl config init", "herdr plugin config-dir huketo.hitl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// DefaultTransports must keep satisfying the seam Run selects by default.
var _ TransportFactory = DefaultTransports
