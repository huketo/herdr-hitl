package cli

import (
	"runtime/debug"
	"strings"
	"time"
)

// fallbackVersion is the released version of this source tree. release-please
// rewrites it on every release through the extra-files entry in
// release-please-config.json — do not edit it by hand.
//
// It exists because the Herdr plugin build cannot stamp a version in. Herdr
// runs `[[build]]` commands as argv with no shell, so the manifest cannot
// compute `-X main.version=$(git describe)`, and a plugin installed at a tag
// would otherwise report itself as "dev".
const fallbackVersion = "0.1.1" // x-release-please-version

// devVersion is what main.go carries when no linker stamp was applied.
const devVersion = "dev"

// Resolve fills in build metadata the linker did not supply.
//
// Three build paths reach this binary and each knows a different amount:
//
//   - goreleaser stamps -X main.version/commit/date. Most precise; always wins.
//   - `go install ...@v0.1.1` stamps nothing, but the module system records the
//     resolved version in the build info.
//   - The Herdr plugin build runs plain `go build` in a git checkout. That
//     records the revision but not the tag, so the version comes from
//     fallbackVersion and the commit from the VCS stamp.
//
// Anything still unknown keeps its zero-ish placeholder rather than inventing
// a value: reporting the wrong version is worse than admitting to none.
func (b BuildInfo) Resolve() BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	return resolveFrom(b, bi, ok)
}

// resolveFrom is Resolve with the build info handed in, so the three build
// paths can be exercised without three builds. A test binary carries no VCS
// stamps at all, which is why this seam exists.
func resolveFrom(b BuildInfo, bi *debug.BuildInfo, ok bool) BuildInfo {
	stamped := b.Version != "" && b.Version != devVersion

	if !ok || bi == nil {
		if !stamped {
			b.Version = fallbackVersion
		}
		return b
	}

	vcs := vcsStamps(bi)
	if !stamped {
		if isReleaseVersion(bi.Main.Version) {
			// The module system already encodes a dirty tree in this string
			// (e.g. "v0.1.1+dirty"), so do not add a second marker.
			b.Version = strings.TrimPrefix(bi.Main.Version, "v")
		} else {
			b.Version = fallbackVersion
			if vcs.modified {
				b.Version += "-dirty"
			}
		}
	}
	if (b.Commit == "" || b.Commit == "none") && vcs.revision != "" {
		b.Commit = shortRevision(vcs.revision)
	}
	if (b.Date == "" || b.Date == "unknown") && vcs.time != "" {
		b.Date = vcs.time
	}
	return b
}

// isReleaseVersion reports whether the module system resolved a real version.
// A binary built from a working tree reports "(devel)", which says nothing.
func isReleaseVersion(v string) bool {
	return v != "" && v != "(devel)"
}

type vcsInfo struct {
	revision string
	time     string
	modified bool
}

func vcsStamps(bi *debug.BuildInfo) vcsInfo {
	var out vcsInfo
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			out.revision = s.Value
		case "vcs.time":
			out.time = s.Value
		case "vcs.modified":
			out.modified = s.Value == "true"
		}
	}
	return out
}

// shortRevision trims a full commit hash to the usual seven characters.
func shortRevision(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// BuildDate parses the recorded date, if it is a timestamp at all.
func (b BuildInfo) BuildDate() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, b.Date)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
