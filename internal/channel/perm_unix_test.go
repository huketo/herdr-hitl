//go:build !windows

package channel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMarkerFileIsPrivate matters because the marker sits in the state
// directory next to the daemon socket and the daemon log, which are
// owner-only for the same reason.
func TestMarkerFileIsPrivate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "away")
	if err := WriteMarker(path, time.Time{}); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("mode = %o, want owner-only", perm)
	}
}
