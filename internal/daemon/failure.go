package daemon

import (
	"io"
	"os"
	"strings"

	"github.com/huketo/herdr-hitl/internal/paths"
)

// failureTailBytes is how much of the end of the daemon log LastFailure reads.
// A startup failure is one or two lines; this is generous and still bounded.
const failureTailBytes = 64 << 10

// LastFailure returns the most recent fatal line the daemon wrote, or "" when
// there is nothing to report.
//
// It exists because the daemon binds the endpoint before it starts its
// transports, which it must: binding is the mutual exclusion that stops two
// daemons polling one bot token. The cost is that a doomed daemon still
// answers a probe, so the CLI's first news of the failure is the connection
// dropping. "connection reset by peer" names the symptom and hides the cause;
// this recovers the cause.
func LastFailure() string {
	path, err := paths.LogFile()
	if err != nil {
		return ""
	}
	return lastFailureIn(path)
}

func lastFailureIn(path string) string {
	f, err := os.Open(path) //nolint:gosec // path is the daemon's own log
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := int64(0)
	if info.Size() > failureTailBytes {
		offset = info.Size() - failureTailBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(tail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if isFailureLine(line) {
			return line
		}
	}
	return ""
}

// isFailureLine recognises the two shapes a fatal daemon message takes: a slog
// record at ERROR level, and the final message `serve` prints before it exits.
func isFailureLine(line string) bool {
	switch {
	case line == "":
		return false
	case strings.Contains(line, "level=ERROR"):
		return true
	case strings.HasPrefix(line, "herdr-hitl: serve:"):
		return true
	default:
		return false
	}
}

// LogSize returns the current length of the daemon log, for use as a baseline
// before spawning. Content past that offset belongs to the new process.
func LogSize() int64 {
	path, err := paths.LogFile()
	if err != nil {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// LastFailureSince returns the most recent fatal line written after offset,
// or "" when the daemon has not failed since then.
//
// The offset matters: a daemon that died last week must not be blamed for a
// daemon that is starting now.
func LastFailureSince(offset int64) string {
	path, err := paths.LogFile()
	if err != nil {
		return ""
	}
	return lastFailureFrom(path, offset)
}

func lastFailureFrom(path string, offset int64) string {
	f, err := os.Open(path) //nolint:gosec // path is the daemon's own log
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return ""
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(tail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); isFailureLine(line) {
			return line
		}
	}
	return ""
}
