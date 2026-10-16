package cli

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// bareVersion is the version alone: the build-time one (-ldflags), else the
// module version the go command stamped from the VCS.
func (b Build) bareVersion() string {
	if b.Version != "0.0.0-dev" {
		return b.Version
	}
	// The go command stamps the module version from the checkout's VCS tag;
	// a "v0.0.0-<pseudo>" one (no tag reachable) we would rather not show.
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
