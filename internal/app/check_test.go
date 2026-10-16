package app

import (
	"strings"
	"testing"

	"github.com/Zesales/hermes-hands/internal/api"
)

func TestCheckLineCompat(t *testing.T) {
	ok := api.CheckResult{Model: "hermes-agent", Base: "https://h"}
	for v, want := range map[string]string{
		"0.22.0": "API OK: hermes-agent @ https://h · hermes-agent 0.22.0",
		"0.20.1": "API OK: hermes-agent @ https://h · hermes-agent 0.20.1\nWARNING: hermes-agent 0.20.1 is older than 0.21.0",
		"0.23.0": "API OK: hermes-agent @ https://h · hermes-agent 0.23.0\nnote: hermes-agent 0.23.0 is newer",
		"":       "API OK: hermes-agent @ https://h\nnote: hermes-agent did not report its version",
	} {
		ok.Version = v
		if got := CheckLine(ok); !strings.HasPrefix(got, want) {
			t.Errorf("CheckLine(%q) = %q, want prefix %q", v, got, want)
		}
	}
	// a session start stays quiet for a supported or unreported version
	if CompatWarning("0.22.2") != "" || CompatWarning("") != "" || CompatWarning("0.20.0") == "" {
		t.Errorf("compatWarning: supported/unknown must be quiet, too-old must warn")
	}
}
