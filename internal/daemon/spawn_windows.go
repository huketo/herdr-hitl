//go:build windows

package daemon

import "syscall"

// detachedProcess is CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS. The group
// keeps a console Ctrl-C from reaching the daemon, and DETACHED_PROCESS gives
// it no console at all, so closing the terminal that started it does not take
// the messenger connections down with it.
const detachedProcess = syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008

// detachAttr detaches the daemon from the console that spawned it.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess}
}

// removeEndpoint is a no-op on Windows: the endpoint is a named pipe, and the
// kernel drops it when the last handle closes.
func removeEndpoint(string) {}
