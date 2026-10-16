package app

import (
	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/compat"
)

// CheckLine is `check` / `/check`'s success line; the server's version is
// named when its /health says it, and a second line says when that version
// is outside the range hermes-hands is built for (internal/compat).
func CheckLine(res api.CheckResult) string {
	line := "API OK: " + res.Model + " @ " + res.Base
	if res.Version != "" {
		line += " · hermes-agent " + res.Version
	}
	switch compat.Check(res.Version) {
	case compat.TooOld:
		line += "\nWARNING: " + compat.Note(res.Version)
	case compat.Newer, compat.Unknown:
		line += "\nnote: " + compat.Note(res.Version)
	}
	return line
}

// CompatWarning is what a session start says about the server's version:
// only a known version outside the range — an unreported one (a /health that
// didn't answer) is left to `check`, not repeated at every start.
func CompatWarning(version string) string {
	switch compat.Check(version) {
	case compat.TooOld, compat.Newer:
		return compat.Note(version)
	}
	return ""
}
