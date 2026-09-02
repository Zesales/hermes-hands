package config

import "testing"

func TestParseAssignments(t *testing.T) {
	in := "" +
		"# a comment\n" +
		"\n" +
		"   \n" +
		"export K1=plain\n" +
		"K2=\"double quoted\"\n" +
		"K3='single quoted'\n" +
		"K4=$'a\\tb\\n'\n" +
		"  export K5=indented\n" +
		"K6=bare\\ with\\ spaces\n" +
		"K7=val # trailing comment dropped\n" +
		"not an assignment line\n" +
		"9BAD=nope\n" +
		"=noname\n" +
		"K1=overwritten\n"

	got := parseAssignments([]byte(in))
	want := map[string]string{
		"K1": "overwritten",
		"K2": "double quoted",
		"K3": "single quoted",
		"K4": "a\tb\n",
		"K5": "indented",
		"K6": "bare with spaces",
		"K7": "val",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys %v, want %d %v", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["9BAD"]; ok {
		t.Errorf("identifier starting with a digit must be rejected")
	}
}

func TestParseValueQuoteForms(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`plain`, "plain"},
		{`"d q"`, "d q"},
		{`"esc \" \\ \$ \` + "`" + `"`, "esc \" \\ $ `"},
		{`'s q'`, "s q"},
		{`'no \n escape'`, `no \n escape`},
		{`$'tab\tend'`, "tab\tend"},
		{`$'hex\x41'`, "hexA"},
		{`$'oct\101'`, "octA"},
		{`bare\ spaced`, "bare spaced"},
		{`cut here`, "cut"},
	} {
		got, ok := parseValue(tc.in)
		if !ok || got != tc.want {
			t.Errorf("parseValue(%q) = %q,%v want %q,true", tc.in, got, ok, tc.want)
		}
	}
	if _, ok := parseValue(`"unterminated`); ok {
		t.Errorf("unterminated double quote should be rejected")
	}
}
