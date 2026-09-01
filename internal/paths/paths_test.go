package paths_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/huketo/herdr-hitl/internal/paths"
)

func TestConfigDirPrecedence(t *testing.T) {
	// HITL_CONFIG_DIR is the escape hatch for local experiments and must beat
	// the directory Herdr injects.
	t.Setenv(paths.EnvHerdrConfigDir, filepath.Join("herdr", "config"))
	t.Setenv(paths.EnvConfigDir, filepath.Join("override", "config"))

	got, err := paths.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if got != filepath.Clean(filepath.Join("override", "config")) {
		t.Fatalf("ConfigDir() = %q, want the HITL_CONFIG_DIR value", got)
	}
}

func TestConfigDirFallsBackToHerdr(t *testing.T) {
	t.Setenv(paths.EnvConfigDir, "")
	t.Setenv(paths.EnvHerdrConfigDir, filepath.Join("herdr", "config"))

	got, err := paths.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if got != filepath.Clean(filepath.Join("herdr", "config")) {
		t.Fatalf("ConfigDir() = %q, want the Herdr-injected value", got)
	}
}

func TestConfigDirFallsBackToXDG(t *testing.T) {
	t.Setenv(paths.EnvConfigDir, "")
	t.Setenv(paths.EnvHerdrConfigDir, "")

	got, err := paths.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if filepath.Base(got) != paths.AppName {
		t.Fatalf("ConfigDir() = %q, want it to end in %q", got, paths.AppName)
	}
}

func TestStateDirPrecedence(t *testing.T) {
	state := t.TempDir()
	t.Setenv(paths.EnvStateDir, state)
	t.Setenv(paths.EnvHerdrStateDir, filepath.Join("herdr", "state"))

	got, err := paths.StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if got != state {
		t.Fatalf("StateDir() = %q, want %q", got, state)
	}
}

func TestSocketDerivesFromTheStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(paths.EnvSocket, "")
	t.Setenv(paths.EnvStateDir, state)

	got, err := paths.Socket()
	if err != nil {
		t.Fatalf("Socket: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(got, `\\.\pipe\`+paths.AppName) {
			t.Fatalf("Socket() = %q, want a named pipe", got)
		}
		return
	}
	if got != filepath.Join(state, "daemon.sock") {
		t.Fatalf("Socket() = %q, want it inside the state dir", got)
	}
}

func TestSocketShortensAnOverlongUnixPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipe names have no sockaddr_un limit")
	}
	// sockaddr_un.sun_path is 104 bytes on macOS and 108 on Linux, and a
	// Herdr plugin state directory can be nested deeply enough to blow it.
	deep := "/tmp/" + strings.Repeat("deeply-nested-directory/", 6) + "state"
	t.Setenv(paths.EnvSocket, "")
	t.Setenv(paths.EnvStateDir, deep)

	got, err := paths.Socket()
	if err != nil {
		t.Fatalf("Socket: %v", err)
	}
	if strings.HasPrefix(got, deep) {
		t.Fatalf("Socket() = %q, want a shortened path outside the deep state dir", got)
	}
	if len(got) > 100 {
		t.Fatalf("Socket() = %q is %d bytes, still too long", got, len(got))
	}
}

func TestSocketHonoursAnExplicitOverride(t *testing.T) {
	t.Setenv(paths.EnvSocket, "/run/custom.sock")

	got, err := paths.Socket()
	if err != nil {
		t.Fatalf("Socket: %v", err)
	}
	if got != "/run/custom.sock" {
		t.Fatalf("Socket() = %q, want the override", got)
	}
}

func TestStateFilesLiveTogether(t *testing.T) {
	state := t.TempDir()
	t.Setenv(paths.EnvStateDir, state)

	for name, fn := range map[string]func() (string, error){
		"daemon.lock": paths.LockFile,
		"daemon.log":  paths.LogFile,
	} {
		got, err := fn()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != filepath.Join(state, name) {
			t.Errorf("%s = %q, want it in the state dir", name, got)
		}
	}
}

func TestConfigFilesLiveTogether(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(paths.EnvConfigDir, dir)

	for name, fn := range map[string]func() (string, error){
		"config.toml": paths.ConfigFile,
		".env":        paths.EnvFile,
	} {
		got, err := fn()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != filepath.Join(dir, name) {
			t.Errorf("%s = %q, want it in the config dir", name, got)
		}
	}
}

func TestEnsureDirIsOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := paths.EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	// Idempotent: the daemon calls it on every start.
	if err := paths.EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir twice: %v", err)
	}
	assertOwnerOnly(t, dir)
}
