package cli

import (
	"regexp"
	"runtime/debug"
	"testing"
)

// semverish matches what release-please writes into fallbackVersion.
var semverish = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func TestFallbackVersionIsAPlainVersion(t *testing.T) {
	t.Parallel()

	// release-please rewrites this constant through the generic updater, which
	// replaces the value on the annotated line. If the literal stops being a
	// bare MAJOR.MINOR.PATCH the annotation is no longer pointing at what we
	// think it is.
	if !semverish.MatchString(fallbackVersion) {
		t.Errorf("fallbackVersion = %q, want a bare MAJOR.MINOR.PATCH", fallbackVersion)
	}
}

func TestResolveKeepsALinkerStamp(t *testing.T) {
	t.Parallel()

	// goreleaser knows the tag, the commit, and the build time. Nothing this
	// package can infer beats that, so a stamped build must pass through
	// untouched.
	in := BuildInfo{Version: "0.1.1", Commit: "abc1234", Date: "2026-09-01T00:00:00Z"}
	got := in.Resolve()
	if got != in {
		t.Errorf("Resolve() = %+v, want the stamp preserved as %+v", got, in)
	}
}

// buildInfo builds a synthetic debug.BuildInfo for one of the three paths.
func buildInfo(mainVersion string, settings map[string]string) *debug.BuildInfo {
	bi := &debug.BuildInfo{Main: debug.Module{Version: mainVersion}}
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return bi
}

func TestResolveFrom(t *testing.T) {
	t.Parallel()

	const rev = "908f4461fbd0e2b0a9a1f4f0c4c9a7a2b1c3d4e5"
	placeholders := BuildInfo{Version: devVersion, Commit: "none", Date: "unknown"}

	tests := []struct {
		name string
		in   BuildInfo
		bi   *debug.BuildInfo
		ok   bool
		want BuildInfo
	}{
		{
			// goreleaser. Knows the tag, the commit, and the build time.
			name: "linker stamp wins",
			in:   BuildInfo{Version: "0.1.1", Commit: "abc1234", Date: "2026-09-01T00:00:00Z"},
			bi:   buildInfo("v9.9.9", map[string]string{"vcs.revision": rev}),
			ok:   true,
			want: BuildInfo{Version: "0.1.1", Commit: "abc1234", Date: "2026-09-01T00:00:00Z"},
		},
		{
			// go install github.com/huketo/herdr-hitl/cmd/herdr-hitl@v0.1.1
			name: "module version when the toolchain resolved one",
			in:   placeholders,
			bi:   buildInfo("v0.2.0", map[string]string{"vcs.revision": rev, "vcs.time": "2026-09-01T07:03:23Z"}),
			ok:   true,
			want: BuildInfo{Version: "0.2.0", Commit: "908f446", Date: "2026-09-01T07:03:23Z"},
		},
		{
			name: "fallback when the toolchain reports devel",
			in:   placeholders,
			bi:   buildInfo("(devel)", map[string]string{"vcs.revision": rev, "vcs.time": "2026-09-01T07:03:23Z"}),
			ok:   true,
			want: BuildInfo{Version: fallbackVersion, Commit: "908f446", Date: "2026-09-01T07:03:23Z"},
		},
		{
			// The Herdr [[build]] command. Herdr clones without tags, so the
			// toolchain synthesises a pseudo-version from the commit — which
			// says no more than the VCS stamp and hides the release.
			name: "fallback beats a pseudo-version",
			in:   placeholders,
			bi: buildInfo("v0.0.0-20260901071544-21f4415ac06e",
				map[string]string{"vcs.revision": rev, "vcs.time": "2026-09-01T07:03:23Z"}),
			ok:   true,
			want: BuildInfo{Version: fallbackVersion, Commit: "908f446", Date: "2026-09-01T07:03:23Z"},
		},
		{
			name: "fallback marks a modified tree",
			in:   placeholders,
			bi:   buildInfo("(devel)", map[string]string{"vcs.revision": rev, "vcs.modified": "true"}),
			ok:   true,
			want: BuildInfo{Version: fallbackVersion + "-dirty", Commit: "908f446", Date: "unknown"},
		},
		{
			// The module system already encodes it; a second marker produced
			// "0.1.1+dirty-dirty".
			name: "module version keeps its own dirty marker",
			in:   placeholders,
			bi:   buildInfo("v0.1.1+dirty", map[string]string{"vcs.revision": rev, "vcs.modified": "true"}),
			ok:   true,
			want: BuildInfo{Version: "0.1.1+dirty", Commit: "908f446", Date: "unknown"},
		},
		{
			name: "no build info at all",
			in:   placeholders,
			bi:   nil,
			ok:   false,
			want: BuildInfo{Version: fallbackVersion, Commit: "none", Date: "unknown"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveFrom(tc.in, tc.bi, tc.ok); got != tc.want {
				t.Errorf("resolveFrom() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIsReleaseVersion(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"":        false,
		"(devel)": false,
		// Synthesised from a commit because the checkout carries no tags,
		// which is every Herdr plugin install.
		"v0.0.0-20260901071544-21f4415ac06e":   false,
		"v0.1.2-0.20260901071544-21f4415ac06e": false,
		"v0.1.1":                               true,
		"v0.1.1+dirty":                         true,
		"v1.2.3-rc.1":                          true,
	}
	for in, want := range tests {
		if got := isReleaseVersion(in); got != want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestShortRevision(t *testing.T) {
	t.Parallel()

	if got := shortRevision("908f4461fbd0e2b0a9a1f4f0c4c9a7a2b1c3d4e5"); got != "908f446" {
		t.Errorf("shortRevision() = %q, want 908f446", got)
	}
	if got := shortRevision("abc"); got != "abc" {
		t.Errorf("shortRevision(short) = %q, want it unchanged", got)
	}
}

func TestBuildDate(t *testing.T) {
	t.Parallel()

	if _, ok := (BuildInfo{Date: "unknown"}).BuildDate(); ok {
		t.Error("BuildDate() accepted a placeholder")
	}
	got, ok := (BuildInfo{Date: "2026-09-01T07:03:23Z"}).BuildDate()
	if !ok {
		t.Fatal("BuildDate() rejected a valid timestamp")
	}
	if got.Year() != 2026 || got.Month() != 9 {
		t.Errorf("BuildDate() = %v", got)
	}
}
