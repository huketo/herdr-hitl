package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// maxDotEnvBytes guards against pointing the loader at a huge file by mistake.
const maxDotEnvBytes = 1 << 20

// loadDotEnv parses a KEY=VALUE file. It follows the same rules as the
// official Herdr plugin examples: blank lines and `#` comments are skipped,
// the split happens at the first `=`, matching single or double quotes are
// stripped, and a missing file is not an error.
//
// Values are returned rather than exported into the process environment:
// a plugin should not leak a bot token into every child process it spawns.
func loadDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // path is a user-owned config file
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > maxDotEnvBytes {
		return nil, fmt.Errorf("%s is %d bytes, which is too large for an env file", path, info.Size())
	}

	out := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		key, value, ok := parseDotEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, line)
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// parseDotEnvLine returns the key and value of one line, or ok=false for
// blanks and comments.
func parseDotEnvLine(raw string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")

	name, rest, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	rest = strings.TrimSpace(rest)

	switch {
	case len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"':
		rest = rest[1 : len(rest)-1]
	case len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'':
		rest = rest[1 : len(rest)-1]
	default:
		// An unquoted value may carry a trailing comment.
		if idx := strings.Index(rest, " #"); idx >= 0 {
			rest = strings.TrimSpace(rest[:idx])
		}
	}
	return name, rest, true
}
