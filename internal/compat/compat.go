// Package compat knows which hermes-agent releases hermes-hands is built for.
//
// hermes-hands uses a small part of hermes-agent's API — the Runs API
// (POST /v1/runs, its SSE events, stop / steer), the session endpoints and
// /v1/capabilities — and relies on how it behaves: a run threaded into a
// session by id, tool and approval events on the stream. That was verified on
// the 0.21 and 0.22 lines only. Older releases were never tested; newer ones
// should work until hermes-agent changes that part of the API (the session
// rework on its main branch, due with 0.23, does — "refuse /v1/runs on a
// natively owned session"). Then Max gets set.
package compat

import (
	"strconv"
	"strings"
)

const (
	// Min is the oldest hermes-agent release hermes-hands supports.
	Min = "0.21.0"
	// Tested is the newest release line hermes-hands was tested on.
	Tested = "0.22"
)

// Status is how a hermes-agent version relates to the supported range.
type Status int

const (
	Unknown   Status = iota // the server didn't say, or not as x.y.z
	Supported               // Min … Tested.x
	TooOld                  // before Min: never tested, not recommended
	Newer                   // after Tested.x: untested, probably fine
)

// Check places version (hermes-agent's own, from GET /health) in the range.
func Check(version string) Status {
	v, ok := parse(version)
	if !ok {
		return Unknown
	}
	minV, _ := parse(Min)
	tested, _ := parse(Tested + ".0")
	switch {
	case less(v, minV):
		return TooOld
	case v[0] > tested[0] || (v[0] == tested[0] && v[1] > tested[1]):
		return Newer
	}
	return Supported
}

// Note is the one line an operator should read about version, "" when it is
// in the supported range.
func Note(version string) string {
	switch Check(version) {
	case TooOld:
		return "hermes-agent " + version + " is older than " + Min + " — not supported: hermes-hands was never tested " +
			"against its Runs API and session handling. Update hermes-agent."
	case Newer:
		return "hermes-agent " + version + " is newer than the tested " + Tested + ".x — it should work until " +
			"hermes-agent changes its Runs / session API; if turns misbehave, suspect that first."
	case Unknown:
		return "hermes-agent did not report its version (GET /health) — hermes-hands is built for " + Min +
			" and newer, tested through " + Tested + ".x."
	}
	return ""
}

// parse reads "0.22.2" (a suffix after the patch number, like "0.22.2rc1",
// is ignored). Calendar-style versions ("2026.9.24") don't parse: they say
// nothing about the API line.
func parse(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 3)
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		n, err := strconv.Atoi(p[:end])
		if err != nil || (i < 2 && end != len(p)) {
			return v, false
		}
		v[i] = n
	}
	if v[0] >= 2000 {
		return v, false
	}
	return v, true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
