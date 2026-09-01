//go:build windows

package daemon

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// lockStale is how long an existing lock file may sit untouched before it is
// treated as abandoned. Windows has no flock, so the lock cannot be released
// by the kernel when its holder dies; a staleness window is what stops a
// killed CLI from blocking daemon startup forever. It only needs to exceed the
// time a spawn takes, which is well under a second.
const lockStale = 30 * time.Second

// tryLock takes an exclusive lock on path using atomic O_EXCL creation. ok
// reports whether the lock was acquired; the returned release removes it.
func tryLock(path string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		if !stale(path) {
			return nil, false, nil
		}
		// Abandoned lock: drop it and make exactly one more attempt, so two
		// racing processes cannot both decide they won.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return nil, false, nil
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if os.IsExist(err) {
			return nil, false, nil
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("create lock file %s: %w", path, err)
	}

	// The pid is diagnostic only; nothing reads it to decide anything.
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")

	return func() {
		_ = f.Close()
		_ = os.Remove(path)
	}, true, nil
}

// stale reports whether an existing lock file looks abandoned.
func stale(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > lockStale
}
