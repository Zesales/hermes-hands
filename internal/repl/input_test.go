package repl

import "testing"

func TestSplitCmd(t *testing.T) {
	for _, tc := range []struct{ in, cmd, rest string }{
		{"", "", ""},
		{"  ", "", ""},
		{"/help", "/help", ""},
		{"/session hh_abc123", "/session", "hh_abc123"},
		{"/session   hh_abc  ", "/session", "hh_abc"},
		{"hello world", "hello", "world"},
		{"just-one-word", "just-one-word", ""},
	} {
		c, r := splitCmd(tc.in)
		if c != tc.cmd || r != tc.rest {
			t.Errorf("splitCmd(%q) = (%q,%q), want (%q,%q)", tc.in, c, r, tc.cmd, tc.rest)
		}
	}
}

func TestIsSlashCmd(t *testing.T) {
	for in, want := range map[string]bool{
		"/quit": true, "/q": true, "/?": true, "/chat-history": true,
		"/etc/hosts": false, "/home/x/repo": false, "hello": false, "/": false, "": false,
	} {
		if got := isSlashCmd(in); got != want {
			t.Errorf("isSlashCmd(%q) = %v, want %v", in, got, want)
		}
	}
}
