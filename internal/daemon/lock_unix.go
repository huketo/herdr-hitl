//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// tryLock takes an exclusive, non-blocking lock on path so that two `ask`
// calls racing to start a daemon spawn exactly one.
//
// flock is the right primitive here because the kernel releases it when the
// holder dies: a crashed CLI cannot leave the lock wedged, which a pid file
// would. The returned release closes the file, and ok reports whether the lock
// was actually acquired.
func tryLock(path string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}

	// The pid is diagnostic only; nothing reads it to decide anything.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
