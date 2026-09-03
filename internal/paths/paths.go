// Package paths resolves the config, state, and socket locations used by both
// the CLI and the daemon.
//
// Every process resolves the same directories, whatever launched it. That is
// a requirement, not a preference: the daemon is a singleton per user because
// Telegram's update queue is single-consumer per bot token, and the socket
// that enforces the singleton is derived from the state directory. A path
// that varies with the caller yields two sockets and two daemons.
//
// Herdr injects HERDR_PLUGIN_CONFIG_DIR and HERDR_PLUGIN_STATE_DIR, but only
// for plugin actions, startup hooks, and event hooks — never for the pane
// process an agent runs `herdr-hitl ask` in. Honouring them would split every
// installation in two: a daemon and config for questions asked by agents, and
// a second set for anything invoked through Herdr. They are therefore
// deliberately ignored here; HerdrConfigDir exposes the value so that doctor
// can warn about a config file stranded there.
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

	// EnvHerdrConfigDir is injected by Herdr for plugin processes. It does
	// not select the config directory; see the package comment.
	EnvHerdrConfigDir = "HERDR_PLUGIN_CONFIG_DIR"
	// EnvHerdrStateDir is injected by Herdr for plugin processes. It does not
	// select the state directory; see the package comment.
	EnvHerdrStateDir = "HERDR_PLUGIN_STATE_DIR"
)

// ConfigDir returns the directory holding config.toml and .env.
func ConfigDir() (string, error) {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
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

// AwayFile returns the path of the Away marker: the file a human creates with
// `herdr-hitl away` to say that questions should leave the terminal and go to
// a messenger.
//
// It lives in the state directory, not the config directory. It is mutable
// runtime state that changes several times a day, and it must be resolved
// identically by every asking process for the same reason the socket must be.
func AwayFile() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "away"), nil
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

// HerdrConfigDir returns the config directory Herdr injected for this process,
// if any. It is not where configuration is read from — it is reported by
// doctor so that a file left there by an older setup is not silently ignored.
func HerdrConfigDir() (string, bool) {
	dir := os.Getenv(EnvHerdrConfigDir)
	if dir == "" {
		return "", false
	}
	return filepath.Clean(dir), true
}
