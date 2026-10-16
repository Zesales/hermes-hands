package turn

import (
	"strings"
	"testing"
)

func TestPreview(t *testing.T) {
	// a final-only round streamed char group by char group
	var got strings.Builder
	fs := &preview{emit: func(s string) { got.WriteString(s) }}
	for _, ch := range []string{`{"cal`, `ls": [], "fin`, `al": "line one`, `\nline `, `two \"q\""`, `}`} {
		fs.feed(ch)
	}
	if got.String() != "line one\nline two \"q\"" {
		t.Errorf("streamed %q", got.String())
	}

	// a tool-call round emits nothing
	got.Reset()
	fs2 := &preview{emit: func(s string) { got.WriteString(s) }}
	for _, ch := range []string{`{"calls": [{"tool":"shell"`, `,"args":{}}], "final": null}`} {
		fs2.feed(ch)
	}
	if got.Len() != 0 {
		t.Errorf("tool-call round should stream nothing, got %q", got.String())
	}

	// the exact chunking a live gateway produced: the chunk ends right at
	// "[" — must wait for the "]" instead of muting the preview as a calls-round
	got.Reset()
	fs3 := &preview{emit: func(s string) { got.WriteString(s) }}
	for _, ch := range []string{`{"`, `calls`, `":[`, `],"`, `final`, `":"`, `OK`, `"}`} {
		fs3.feed(ch)
	}
	if got.String() != "OK" {
		t.Errorf("live chunking streamed %q, want OK", got.String())
	}
	// …and a real calls-round split the same way still streams nothing
	got.Reset()
	fs4 := &preview{emit: func(s string) { got.WriteString(s) }}
	for _, ch := range []string{`{"`, `calls`, `":[`, `{"id":"c1","tool":"shell"}],"`, `final`, `":null}`} {
		fs4.feed(ch)
	}
	if got.Len() != 0 {
		t.Errorf("split calls-round streamed %q", got.String())
	}

	// an escape split by the chunk boundary waits for its second half. The
	// live case (2026-10-16): `\n` cut after the `\` printed a literal `\n`,
	// and a `\\\"` cut after its first `\` ended the preview at the `\"` —
	// the answer was then printed a second time.
	for _, tc := range []struct {
		chunks []string
		want   string
	}{
		{[]string{`{"calls":[],"final":"a\`, `nb\`, `"c\`, `\\"d"}`}, "a\nb\"c\\\"d"},
		{[]string{`{"calls":[],"final":"\u00`, `e4 \ud83d`, `\ude00 x\`, `u0041"}`}, "ä 😀 xA"},
		{[]string{`{"calls":[],"final":"tab\`, `t cr\`, `r"}`}, "tab\t cr\r"},
	} {
		got.Reset()
		f := &preview{emit: func(s string) { got.WriteString(s) }}
		for _, ch := range tc.chunks {
			f.feed(ch)
		}
		if got.String() != tc.want {
			t.Errorf("chunks %q streamed %q, want %q", tc.chunks, got.String(), tc.want)
		}
	}

	// "final": null ahead of the calls streams nothing (it used to preview
	// the next key's name)
	got.Reset()
	fs5 := &preview{emit: func(s string) { got.WriteString(s) }}
	for _, ch := range []string{`{"final": `, `null, "calls": [{"tool":"local_shell"}]}`} {
		fs5.feed(ch)
	}
	if got.Len() != 0 {
		t.Errorf("final-first calls-round streamed %q", got.String())
	}

	// reset keeps the emit fn, drops state
	fs.reset()
	if fs.started || fs.done || fs.pos != 0 || fs.emit == nil {
		t.Errorf("reset left state: %+v", fs)
	}
}
