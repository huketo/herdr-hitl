package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// manifestVersion pulls the top-level `version` out of herdr-plugin.toml
// without pulling in a TOML parser for one line.
var manifestVersion = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"`)

// semver is deliberately strict: Herdr parses this field, and release-please
// writes it, so anything else means one of them is about to be surprised.
var semver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// TestPluginManifestVersionTracksTheReleaseManifest guards a silent failure.
//
// release-please owns both `.release-please-manifest.json` and the `version`
// in `herdr-plugin.toml`, the latter through an `extra-files` entry with a
// jsonpath. If that jsonpath ever stops matching — the key is renamed, moved
// under a table, or the entry is dropped — release-please does not complain.
// It updates the JSON, skips the TOML, and every subsequent release ships a
// plugin that reports a version it is not. Herdr shows that version to the
// user and `plugin install` records it.
//
// Comparing the two files is the only check that notices.
func TestPluginManifestVersionTracksTheReleaseManifest(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, ".release-please-manifest.json"))
	if err != nil {
		t.Skipf("release manifest unavailable: %v", err)
	}
	var released map[string]string
	if err := json.Unmarshal(raw, &released); err != nil {
		t.Fatalf("parse .release-please-manifest.json: %v", err)
	}
	want, ok := released["."]
	if !ok {
		t.Fatal(`.release-please-manifest.json has no "." entry`)
	}

	pluginTOML, err := os.ReadFile(filepath.Join(root, "herdr-plugin.toml"))
	if err != nil {
		t.Skipf("plugin manifest unavailable: %v", err)
	}
	m := manifestVersion.FindSubmatch(pluginTOML)
	if m == nil {
		t.Fatal("herdr-plugin.toml has no top-level `version = \"...\"`; " +
			"the release-please extra-files jsonpath $.version cannot match it")
	}
	got := string(m[1])

	if got != want {
		t.Errorf("herdr-plugin.toml version = %q, release manifest says %q; "+
			"release-please should keep these equal via the extra-files entry in release-please-config.json",
			got, want)
	}
	if !semver.MatchString(got) {
		t.Errorf("herdr-plugin.toml version = %q, want a bare MAJOR.MINOR.PATCH", got)
	}
}

// TestReleaseConfigUpdatesThePluginManifest asserts the wiring that the test
// above depends on actually exists.
func TestReleaseConfigUpdatesThePluginManifest(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "release-please-config.json"))
	if err != nil {
		t.Skipf("release config unavailable: %v", err)
	}

	var cfg struct {
		Packages map[string]struct {
			ExtraFiles []struct {
				Type     string `json:"type"`
				Path     string `json:"path"`
				JSONPath string `json:"jsonpath"`
			} `json:"extra-files"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse release-please-config.json: %v", err)
	}

	root, ok := cfg.Packages["."]
	if !ok {
		t.Fatal(`release-please-config.json has no "." package`)
	}
	want := map[string]string{
		// The manifest version Herdr shows the user.
		"herdr-plugin.toml": "toml",
		// The version a build without a linker stamp falls back to, which is
		// every Herdr plugin build.
		"internal/cli/buildinfo.go": "generic",
	}
	for _, f := range root.ExtraFiles {
		kind, tracked := want[f.Path]
		if !tracked {
			continue
		}
		if f.Type != kind {
			t.Errorf("extra-files entry for %s has type %q, want %q", f.Path, f.Type, kind)
		}
		if f.Path == "herdr-plugin.toml" && f.JSONPath != "$.version" {
			t.Errorf("extra-files entry for %s has jsonpath %q, want \"$.version\"", f.Path, f.JSONPath)
		}
		delete(want, f.Path)
	}
	for path := range want {
		t.Errorf("release-please-config.json does not list %s in extra-files, "+
			"so its version will stop tracking releases", path)
	}
}

// repoRoot walks up from the package directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find the module root from " + strings.TrimSpace(dir))
	return ""
}

// TestFallbackVersionTracksTheReleaseManifest is the buildinfo half of the
// same invariant: release-please rewrites the annotated constant, and a
// mismatch means the annotation stopped matching and every plugin build now
// reports a stale version.
func TestFallbackVersionTracksTheReleaseManifest(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".release-please-manifest.json"))
	if err != nil {
		t.Skipf("release manifest unavailable: %v", err)
	}
	var released map[string]string
	if err := json.Unmarshal(raw, &released); err != nil {
		t.Fatalf("parse .release-please-manifest.json: %v", err)
	}
	if want := released["."]; fallbackVersion != want {
		t.Errorf("fallbackVersion = %q, release manifest says %q; "+
			"the x-release-please-version annotation in buildinfo.go is not being applied",
			fallbackVersion, want)
	}
}
