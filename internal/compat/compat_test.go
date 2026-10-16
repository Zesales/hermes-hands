package compat

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    Status
	}{
		{"0.21.0", Supported},
		{"0.21.5", Supported},
		{"0.22.0", Supported},
		{"0.22.2", Supported},
		{"v0.22.2", Supported},
		{"0.22.2rc1", Supported},
		{"0.22.99", Supported},
		{"0.20.6", TooOld},
		{"0.9.0", TooOld},
		{"0.23.0", Newer},
		{"1.0.0", Newer},
		{"", Unknown},
		{"2026.9.24", Unknown}, // calendar-era tag: says nothing about the API line
		{"0.22", Unknown},
		{"0.x.1", Unknown},
		{"latest", Unknown},
	} {
		if got := Check(tc.version); got != tc.want {
			t.Errorf("Check(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestNote(t *testing.T) {
	if n := Note("0.22.2"); n != "" {
		t.Errorf("supported version has a note: %q", n)
	}
	for v, want := range map[string]string{
		"0.20.6": "not supported",
		"0.23.1": "newer than the tested 0.22.x",
		"":       "did not report its version",
	} {
		if n := Note(v); !strings.Contains(n, want) {
			t.Errorf("Note(%q) = %q, want it to say %q", v, n, want)
		}
	}
}
