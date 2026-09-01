//go:build !windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// Listen binds the daemon endpoint.
//
// A Unix socket file survives a crash, so a leftover file would permanently
// block startup. Listen therefore probes the existing socket: if nothing
// answers it is stale and gets removed, and if something does answer the
// caller is told a daemon is already running.
func Listen(endpoint string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	// The daemon listener outlives every request context by design, so it is
	// bound against Background rather than a caller's context.
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "unix", endpoint)
	if err == nil {
		if chmodErr := os.Chmod(endpoint, 0o600); chmodErr != nil {
			_ = l.Close()
			return nil, fmt.Errorf("restrict socket permissions: %w", chmodErr)
		}
		return l, nil
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("listen on %s: %w", endpoint, err)
	}

	if Probe(context.Background(), endpoint) {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, endpoint)
	}
	if err := os.Remove(endpoint); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %s: %w", endpoint, err)
	}
	l, err = lc.Listen(context.Background(), "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", endpoint, err)
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("restrict socket permissions: %w", err)
	}
	return l, nil
}

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", endpoint)
}
