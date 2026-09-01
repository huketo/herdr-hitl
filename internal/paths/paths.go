// Package paths resolves the config, state, and socket locations used by both
// the CLI and the daemon.
//
// Herdr owns the directories when herdr-hitl runs as a plugin
// (HERDR_PLUGIN_CONFIG_DIR / HERDR_PLUGIN_STATE_DIR). Outside Herdr the
// binary is still a normal CLI, so it falls back to the XDG layout.
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AppName is the directory name used for XDG fallbacks.
const AppName = "herdr-hitl"

// Environment overrides, highest precedence first.
const (
	// EnvConfigDir overrides the config directory for local experiments.
	EnvConfigDir = "HITL_CONFIG_DIR"
	// EnvStateDir overrides the state directory.
	EnvStateDir = "HITL_STATE_DIR"
	// EnvSocket overrides the daemon endpoint outright.
	EnvSocket = "HITL_SOCKET"

	// EnvHerdrConfigDir is injected by Herdr for plugin processes.
	EnvHerdrConfigDir = "HERDR_PLUGIN_CONFIG_DIR"
	// EnvHerdrStateDir is injected by Herdr for plugin processes.
	EnvHerdrStateDir = "HERDR_PLUGIN_STATE_DIR"
)

// ConfigDir returns the directory holding config.toml and .env.
func ConfigDir() (string, error) {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
		return filepath.Clean(dir), nil
	}
	if dir := os.Getenv(EnvHerdrConfigDir); dir != "" {
		return filepath.Clean(dir), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, AppName), nil
}

// StateDir returns the directory holding the socket, lock file, poll cursor,
// and answer log.
func StateDir() (string, error) {
	if dir := os.Getenv(EnvStateDir); dir != "" {
		return filepath.Clean(dir), nil
	}
	if dir := os.Getenv(EnvHerdrStateDir); dir != "" {
		return filepath.Clean(dir), nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve user cache dir: %w", err)
		}
		return filepath.Join(base, AppName), nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, AppName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", AppName), nil
}

// EnsureDir creates dir with owner-only permissions. Secrets and sockets live
// here, so 0o700 is the point, not a detail.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// Socket returns the daemon endpoint: a Unix socket path on Unix, a named pipe
// name on Windows. The name is derived from the state directory so that two
// Herdr sessions with different plugin state directories never collide.
func Socket() (string, error) {
	if s := os.Getenv(EnvSocket); s != "" {
		return s, nil
	}
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		return `\\.\pipe\` + AppName + "-" + fingerprint(state), nil
	}
	// Unix domain socket paths are capped near 104 bytes on macOS and 108 on
	// Linux. A deep Herdr state directory can blow that, so fall back to a
	// short hashed path under the user's runtime dir when needed.
	sock := filepath.Join(state, "daemon.sock")
	if len(sock) <= 90 {
		return sock, nil
	}
	return filepath.Join(shortRuntimeDir(), AppName+"-"+fingerprint(state)+".sock"), nil
}

// LockFile returns the lock a running daemon holds for its whole lifetime.
//
// Holding it is what makes "one daemon per machine" true. The socket path
// cannot carry that guarantee on its own: a daemon unlinks the socket file as
// it shuts down, and a daemon that started in the meantime would have its
// file deleted out from under it, leaving two live daemons and two pollers on
// one bot token.
func LockFile() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "daemon.lock"), nil
}

// SpawnLockFile returns the lock that serialises daemon spawning.
//
// It is deliberately not LockFile: the spawning CLI holds this one while it
// waits for the child to come up, and the child holds LockFile for its
// lifetime. One file for both would deadlock — the parent would still be
// holding it when the child tried to take it.
func SpawnLockFile() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "spawn.lock"), nil
}

// LogFile returns the path the background daemon writes its log to.
func LogFile() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "daemon.log"), nil
}

// ConfigFile returns the path of config.toml.
func ConfigFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// EnvFile returns the path of the .env file, which mirrors the convention used
// by the official Herdr plugin examples.
func EnvFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".env"), nil
}

func shortRuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:5])
}
