package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseArgsTable(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want parsed
	}{
		{"none", nil, parsed{smode: "new"}},
		{"help short", []string{"-h"}, parsed{smode: "new", action: "help"}},
		{"help long", []string{"--help"}, parsed{smode: "new", action: "help"}},
		{"version -v", []string{"-v"}, parsed{smode: "new", action: "version"}},
		{"version word", []string{"version"}, parsed{smode: "new", action: "version"}},
		{"check", []string{"check"}, parsed{smode: "new", action: "check"}},
		{"check flag", []string{"--check"}, parsed{smode: "new", action: "check"}},
		{"sessions", []string{"sessions"}, parsed{smode: "new", action: "sessions"}},
		{"list flag", []string{"--list"}, parsed{smode: "new", action: "sessions"}},
		{"setup", []string{"setup"}, parsed{smode: "new", action: "setup"}},
		{"continue", []string{"-c"}, parsed{smode: "continue"}},
		{"continue long", []string{"--continue"}, parsed{smode: "continue"}},
		{"new explicit", []string{"--new"}, parsed{smode: "new"}},
		{"session id", []string{"--session", "hh_abc"}, parsed{smode: "hh_abc"}},
		{"session missing id", []string{"--session"}, parsed{smode: "new", errMsg: "--session needs an id"}},
		{"yolo", []string{"--yolo"}, parsed{smode: "new", yolo: true}},
		{"stdin dash", []string{"-"}, parsed{smode: "new", stdin: true}},
		{"oneshot words", []string{"why", "is", "ci", "red"}, parsed{smode: "new", oneshot: []string{"why", "is", "ci", "red"}}},
		{"double dash rest", []string{"--", "check", "--help"}, parsed{smode: "new", oneshot: []string{"check", "--help"}}},
		{"unknown option", []string{"--bogus"}, parsed{smode: "new", errMsg: "unknown option: --bogus"}},
		{"eager check wins mid-parse", []string{"foo", "check"}, parsed{smode: "new", action: "check", oneshot: []string{"foo"}}},
		{"continue then oneshot", []string{"-c", "and", "fix"}, parsed{smode: "continue", oneshot: []string{"and", "fix"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseArgs(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseArgs(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestUsageTextByteExact(t *testing.T) {
	// The last line must not carry an extra trailing blank line (matches
	// `cat <<'EOF'`).
	if !strings.HasSuffix(usageText, "  --new   force a fresh session      --yolo   skip run/write approvals\n") {
		t.Errorf("usage text tail is wrong:\n%q", usageText[len(usageText)-90:])
	}
	if !strings.HasPrefix(usageText, "hermes-hands - terminal chat with a central Hermes brain over its Runs API.\n") {
		t.Errorf("usage text head is wrong")
	}
	if strings.Contains(usageText, "\n\n\n") {
		t.Errorf("usage text has a triple newline")
	}
}

func TestVersionString(t *testing.T) {
	// With the build-time default and no ldflags, the git sha (if any) is
	// appended in parentheses; without it, just the bare version.
	got := versionString()
	if !strings.HasPrefix(got, "hermes-hands ") {
		t.Errorf("versionString = %q", got)
	}
	if strings.Contains(got, "(") && !strings.HasSuffix(got, ")") {
		t.Errorf("unbalanced sha parens: %q", got)
	}
}

func TestShellQuoteQRoundTrips(t *testing.T) {
	for _, v := range []string{
		"https://hermes-api.example.net",
		"sk-abcDEF123._-",
		"has space",
		"quote'inside",
		"dollar$and&amp",
		"tab\tnewline\n",
		"",
	} {
		q := shellQuoteQ(v)
		if q == "" {
			t.Errorf("shellQuoteQ(%q) produced empty output", v)
		}
		// A bare (unquoted) result must have no unescaped shell metacharacters.
		if !strings.HasPrefix(q, "'") && !strings.HasPrefix(q, "$'") {
			for _, meta := range []string{" ", "\t", "'", "$", "&", "\n"} {
				if strings.Contains(v, meta) && strings.Contains(q, meta) && !strings.Contains(q, "\\"+meta) {
					t.Errorf("shellQuoteQ(%q) = %q leaves %q unescaped", v, q, meta)
				}
			}
		}
	}
}
