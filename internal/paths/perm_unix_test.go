//go:build !windows

package paths_test

import (
	"os"
	"testing"
)

// assertOwnerOnly checks the 0o700 bit pattern that keeps a bot token and the
// daemon socket away from other local users.
func assertOwnerOnly(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %o, want 700", perm)
	}
}
