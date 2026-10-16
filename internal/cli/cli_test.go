package cli

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
		{"none", nil, parsed{}},
		{"help short", []string{"-h"}, parsed{action: "help"}},
		{"help long", []string{"--help"}, parsed{action: "help"}},
		{"version -v", []string{"-v"}, parsed{action: "version"}},
		{"version word", []string{"version"}, parsed{action: "version"}},
		{"check", []string{"check"}, parsed{action: "check"}},
		{"check flag", []string{"--check"}, parsed{action: "check"}},
		{"sessions", []string{"sessions"}, parsed{action: "sessions"}},
		{"list flag", []string{"--list"}, parsed{action: "sessions"}},
		{"setup", []string{"setup"}, parsed{action: "setup"}},
		{"session-list flag", []string{"--session-list"}, parsed{action: "sessions"}},
		{"-c is gone", []string{"-c"}, parsed{errMsg: "unknown option: -c"}},
		{"--continue is gone", []string{"--continue"}, parsed{errMsg: "unknown option: --continue"}},
		{"new explicit", []string{"--new"}, parsed{smode: "new"}},
		{"rpc", []string{"--rpc"}, parsed{rpc: true}},
		{"session bare needs an id", []string{"--session"}, parsed{errMsg: "--session needs a session id (ids from --session-list)"}},
		{"session id", []string{"--session", "hh_abc"}, parsed{smode: "hh_abc"}},
		{"session then flag needs an id", []string{"--session", "--yolo"}, parsed{errMsg: "--session needs a session id (ids from --session-list)", yolo: true}},
		{"sessions new", []string{"sessions", "new"}, parsed{action: "sessionnew"}},
		{"sessions list", []string{"sessions"}, parsed{action: "sessions"}},
		{"session-list verbose", []string{"--session-list-verbose"}, parsed{action: "sessions", verbose: true}},
		{"sessions -v", []string{"sessions", "-v"}, parsed{action: "sessions", verbose: true}},
		{"session-list --verbose", []string{"--session-list", "--verbose"}, parsed{action: "sessions", verbose: true}},
		{"yolo", []string{"--yolo"}, parsed{yolo: true}},
		{"unknown option", []string{"--bogus"}, parsed{errMsg: "unknown option: --bogus"}},
		{"stray argument", []string{"why is ci red"}, parsed{errMsg: "unexpected argument: why is ci red (there is no one-shot; start a session)"}},
		{"positional before subcommand still errors", []string{"foo", "check"}, parsed{errMsg: "unexpected argument: foo (there is no one-shot; start a session)"}},
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

func TestUsageTextShape(t *testing.T) {
	if !strings.HasPrefix(usageText, "hermes-hands - terminal chat with a central Hermes brain over its Runs API.\n") {
		t.Errorf("usage text head is wrong")
	}
	if !strings.HasSuffix(usageText, "\n") || strings.HasSuffix(usageText, "\n\n") {
		t.Errorf("usage text must end with exactly one newline")
	}
	if strings.Contains(usageText, "\n\n\n") {
		t.Errorf("usage text has a triple newline")
	}
	for _, want := range []string{"--rpc", "sessions new", "/setup in-session", "/yolo in-session"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text missing %q", want)
		}
	}
	if strings.Contains(usageText, "hermes-hands -c") {
		t.Errorf("usage text still documents the removed -c flag")
	}
}

func TestVersionString(t *testing.T) {
	// With the build-time default and no ldflags, the git sha (if any) is
	// appended in parentheses; without it, just the bare version.
	got := Build{Version: "0.0.0-dev"}.versionString()
	if !strings.HasPrefix(got, "hermes-hands ") {
		t.Errorf("versionString = %q", got)
	}
	if strings.Contains(got, "(") && !strings.HasSuffix(got, ")") {
		t.Errorf("unbalanced sha parens: %q", got)
	}
}
