//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// detachAttr puts the daemon in its own session so it survives the terminal
// (or Herdr pane) that spawned it: no controlling tty means no SIGHUP when the
// pane closes, and no SIGINT when the agent gets Ctrl-C'd.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// removeEndpoint deletes the socket file. A Unix socket outlives its process,
// and a leftover file makes the next startup look like a running daemon until
// it is probed. A failure needs no handling: ipc.Listen probes and removes
// stale sockets on the next start.
func removeEndpoint(endpoint string) {
	_ = os.Remove(endpoint)
}
