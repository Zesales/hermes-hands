package cli

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// bareVersion is the version alone: the build-time one, else a module
// version `go install` stamped.
func (b Build) bareVersion() string {
	if b.Version != "0.0.0-dev" {
		return b.Version
	}
	// go install <module>@vX.Y.Z sets a real tag here; a plain local `go build`
	// sets a "v0.0.0-<pseudo>" string we would rather not show.
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" && !strings.HasPrefix(v, "v0.0.0-") {
			return v
		}
	}
	return b.Version
}

// versionString is `hermes-hands --version`: the version and the commit.
func (b Build) versionString() string {
	ver, sha := b.bareVersion(), b.Commit
	if sha == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					sha = s.Value[:7]
				}
			}
		}
	}
	if sha != "" {
		return fmt.Sprintf("hermes-hands %s (%s)", ver, sha)
	}
	return "hermes-hands " + ver
}
