package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLastFailureIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		log  string
		want string
	}{
		{
			name: "startup failure is the last word",
			log: `time=2026-09-01T12:51:15.100+09:00 level=INFO msg="daemon listening"
time=2026-09-01T12:51:15.366+09:00 level=ERROR msg="transport failed to start" transport=telegram error="telegram: delete webhook: unexpected end of JSON input"
herdr-hitl: serve: hitl: no transport available: no transport could be started
`,
			want: "herdr-hitl: serve: hitl: no transport available: no transport could be started",
		},
		{
			name: "an error record counts even without a serve line",
			log: `time=2026-09-01T12:51:15.100+09:00 level=INFO msg="daemon listening"
time=2026-09-01T12:51:15.366+09:00 level=ERROR msg="transport failed to start" transport=discord
`,
			want: `time=2026-09-01T12:51:15.366+09:00 level=ERROR msg="transport failed to start" transport=discord`,
		},
		{
			name: "a healthy log reports nothing",
			log: `time=2026-09-01T12:51:15.100+09:00 level=INFO msg="daemon listening"
time=2026-09-01T12:51:16.000+09:00 level=WARN msg="settle failed"
`,
			want: "",
		},
		{
			name: "empty log",
			log:  "",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "daemon.log")
			if err := os.WriteFile(path, []byte(tc.log), 0o600); err != nil {
				t.Fatalf("write log: %v", err)
			}
			if got := lastFailureIn(path); got != tc.want {
				t.Errorf("lastFailureIn() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLastFailureInMissingLog(t *testing.T) {
	t.Parallel()

	if got := lastFailureIn(filepath.Join(t.TempDir(), "absent.log")); got != "" {
		t.Errorf("lastFailureIn(absent) = %q, want empty", got)
	}
}

func TestLastFailureReadsOnlyTheTail(t *testing.T) {
	t.Parallel()

	// A daemon that has run for weeks has a large log. Reading all of it to
	// explain one failed ask would be wasteful; only the tail is scanned.
	path := filepath.Join(t.TempDir(), "daemon.log")
	noise := strings.Repeat("time=x level=INFO msg=\"question delivered\"\n", 40_000)
	body := noise + "herdr-hitl: serve: boom\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if len(body) <= failureTailBytes {
		t.Fatalf("test log is %d bytes, which does not exceed the %d byte tail", len(body), failureTailBytes)
	}
	if got := lastFailureIn(path); got != "herdr-hitl: serve: boom" {
		t.Errorf("lastFailureIn() = %q", got)
	}
}
