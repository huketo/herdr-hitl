//go:build windows

package paths_test

import (
	"os"
	"testing"
)

// assertOwnerOnly only checks existence on Windows: NTFS ACLs do not map onto
// Unix permission bits, and Go's os package does not model them.
func assertOwnerOnly(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
}
