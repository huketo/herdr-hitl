package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// newInstallCLICommand exposes the binary outside the plugin checkout. Herdr
// builds the plugin inside its managed checkout, which is not on the agent's
// PATH, so without this the agent cannot invoke `herdr-hitl` at all.
func newInstallCLICommand(_ *globals) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "install-cli",
		Short: "Link the herdr-hitl binary into a directory on PATH",
		Long: "Herdr builds the plugin binary inside its managed checkout, which is\n" +
			"not on PATH. This links (or, on Windows, copies) the running executable\n" +
			"into a directory an agent can reach.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInstallCLI(cmd, dir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "target directory (default: "+defaultInstallDir()+")")
	return cmd
}

func runInstallCLI(cmd *cobra.Command, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return failf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if dir == "" {
		dir = defaultInstallDir()
	}
	if dir == "" {
		return usagef("install-cli: cannot determine a target directory, pass --dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failf("create %s: %w", dir, err)
	}

	target := filepath.Join(dir, "herdr-hitl")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if sameFile(exe, target) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s is already installed\n", target)
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failf("replace %s: %w", target, err)
	}

	if runtime.GOOS == "windows" {
		// Symlinks need developer mode or elevation on Windows; copying is
		// the only reliable option.
		if err := copyExecutable(exe, target); err != nil {
			return failf("copy %s: %w", target, err)
		}
	} else if err := os.Symlink(exe, target); err != nil {
		return failf("link %s: %w", target, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, target)
	if !onPath(dir) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is not on PATH; add it to use `herdr-hitl` by name\n", dir)
	}
	return nil
}

// defaultInstallDir is the conventional per-user binary directory.
func defaultInstallDir() string {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "Programs", "herdr-hitl")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is our own executable
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) //nolint:gosec // an executable must be executable
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// sameFile reports whether target already resolves to src, so a repeated
// install is a no-op instead of a delete-and-relink of a running binary.
func sameFile(src, target string) bool {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	a, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	b, err := os.Stat(src)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

func onPath(dir string) bool {
	clean := filepath.Clean(dir)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		if strings.EqualFold(filepath.Clean(entry), clean) {
			return true
		}
	}
	return false
}
