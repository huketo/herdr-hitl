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
	for _, f := range root.ExtraFiles {
		if f.Path != "herdr-plugin.toml" {
			continue
		}
		if f.Type != "toml" {
			t.Errorf("extra-files entry for %s has type %q, want \"toml\"", f.Path, f.Type)
		}
		if f.JSONPath != "$.version" {
			t.Errorf("extra-files entry for %s has jsonpath %q, want \"$.version\"", f.Path, f.JSONPath)
		}
		return
	}
	t.Error("release-please-config.json does not list herdr-plugin.toml in extra-files, " +
		"so the plugin version will stop tracking releases")
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
