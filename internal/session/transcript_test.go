package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFitRoundUnderTheLimitIsUntouched(t *testing.T) {
	outs := []string{"a", "bb"}
	got := FitRound(outs, []int{1, 2}, 100)
	if got[0] != "a" || got[1] != "bb" {
		t.Errorf("got %q", got)
	}
	if got := FitRound([]string{strings.Repeat("x", 500)}, []int{1}, 0); len(got[0]) != 500 {
		t.Errorf("limit 0 = no limit, got %d bytes", len(got[0]))
	}
}

// The small outputs stay whole; the big ones share what is left and keep
// head and tail around a marker that says how to read the cut part back.
func TestFitRoundSharesTheBudget(t *testing.T) {
	small := "ok\n"
	big := strings.Repeat("h", 9000) + strings.Repeat("t", 9000)
	got := FitRound([]string{small, big, big}, []int{7, 8, 9}, 8000)
	if got[0] != small {
		t.Errorf("the small output was cut: %q", got[0])
	}
	for i, ref := range []int{8, 9} {
		o := got[i+1]
		if len(o) > 4600 {
			t.Errorf("#%d kept %d bytes, over its share", ref, len(o))
		}
		if !strings.HasPrefix(o, "hhh") || !strings.HasSuffix(o, "ttt") {
			t.Errorf("#%d lost its head or tail", ref)
		}
		if !strings.Contains(o, `local_output {"ref":`+itoa(ref)+`,"offset":`) {
			t.Errorf("#%d: marker does not say how to read it back:\n%s", ref, o[2000:2600])
		}
	}
}

func TestFitRoundCutsOnRuneBoundaries(t *testing.T) {
	big := strings.Repeat("ä", 5000)
	for _, o := range FitRound([]string{big}, []int{1}, 3000) {
		if !utf8.ValidString(o) {
			t.Fatal("cut through a UTF-8 sequence")
		}
	}
}

// a two-round turn: a call, its result, the final — as the loop records it
func roundTurn(turn, seq int, out string) []Event {
	ok := true
	return []Event{
		{Seq: seq, Turn: turn, Kind: KindUser, Text: "why is CI red?"},
		{Seq: seq + 1, Turn: turn, Kind: KindReply, Round: 1, Text: "Let me look at the workflow.\n" + `{"calls":[{"id":"c1","tool":"local_shell","args":{"cmd":"cat ci.yml"}}],"final":null}`},
		{Seq: seq + 2, Turn: turn, Kind: KindCall, Round: 1, Ref: turn, ID: "c1", Tool: "local_shell", Args: json.RawMessage(`{"cmd":"cat ci.yml"}`)},
		{Seq: seq + 3, Turn: turn, Kind: KindResult, Round: 1, Ref: turn, Tool: "local_shell", Exit: Int(0), Bytes: len(out), Lines: 1},
		{Seq: seq + 4, Turn: turn, Kind: KindReply, Round: 2, Text: `{"calls":[],"final":"the lockfile is missing"}`},
		{Seq: seq + 5, Turn: turn, Kind: KindAnswer, Text: "the lockfile is missing", OK: &ok},
		{Seq: seq + 6, Turn: turn, Kind: KindTurnEnd},
	}
}

func roles(msgs []Msg) string {
	var r []string
	for _, m := range msgs {
		r = append(r, m.Role)
	}
	return strings.Join(r, " ")
}

func TestTranscriptVerbatim(t *testing.T) {
	out := strings.Repeat("step: build\n", 100)
	msgs := Transcript(roundTurn(1, 1, out), TranscriptOpts{Output: func(int) string { return out }})
	if got := roles(msgs); got != "user assistant user assistant" {
		t.Fatalf("roles = %s", got)
	}
	if msgs[0].Content != "[operator] why is CI red?" {
		t.Errorf("operator = %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[1].Content, "Let me look at the workflow.") {
		t.Errorf("reply lost its prose: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[2].Content, `"ref":1`) || !strings.Contains(msgs[2].Content, "step: build") {
		t.Errorf("results not verbatim: %.200s", msgs[2].Content)
	}
	if !strings.Contains(msgs[3].Content, `"final":"the lockfile is missing"`) {
		t.Errorf("final reply = %q", msgs[3].Content) // the answer is not added a second time
	}
}

// Elided: a batch's results become one line per call — #ref, what ran, how
// it ended, its size and the gist; a small output costs less than its line
// and stays.
func TestTranscriptElided(t *testing.T) {
	big := strings.Repeat("x", 5000)
	evs := append(roundTurn(1, 1, big), roundTurn(2, 8, "ok")...)
	msgs := Transcript(evs, TranscriptOpts{Upto: 14, Output: func(ref int) string {
		if ref == 1 {
			return big
		}
		return "ok"
	}})
	if got := roles(msgs); got != "user assistant user assistant user assistant user assistant" {
		t.Fatalf("roles = %s", got)
	}
	if strings.Contains(msgs[2].Content, big) || !strings.Contains(msgs[2].Content, `local_output {"ref":N} reads any of them back`) ||
		!strings.Contains(msgs[2].Content, "\n#1 local_shell `cat ci.yml` → exit 0 · 1 line · 5 KB · xxx") {
		t.Errorf("big output not elided to a line: %.300s", msgs[2].Content)
	}
	if !strings.HasSuffix(msgs[6].Content, "#2 local_shell `cat ci.yml` → exit 0: ok") {
		t.Errorf("small output should stay: %s", msgs[6].Content)
	}
}

// A failed command's line carries its error line and how it ended.
func TestTranscriptElidedGist(t *testing.T) {
	out := "=== RUN TestA\n" + strings.Repeat("noise\n", 80) + "loop_test.go:88: FAIL: want 2, got 3\n" + strings.Repeat("more\n", 20) + "FAIL hermes-hands/internal/loop 0.4s\n"
	ok := false
	evs := []Event{
		{Seq: 1, Turn: 1, Kind: KindUser, Text: "test"},
		{Seq: 2, Turn: 1, Kind: KindCall, Ref: 3, ID: "c1", Tool: "local_shell", Args: json.RawMessage(`{"cmd":"go test ./..."}`)},
		{Seq: 3, Turn: 1, Kind: KindResult, Ref: 3, Exit: Int(1), Bytes: len(out), Lines: 103},
		{Seq: 4, Turn: 1, Kind: KindAnswer, Text: "a test fails", OK: &ok},
	}
	msgs := Transcript(evs, TranscriptOpts{Upto: 4, Output: func(int) string { return out }})
	want := "#3 local_shell `go test ./...` → exit 1 · 103 lines · 668 B · loop_test.go:88: FAIL: want 2, got 3 ⏎ FAIL hermes-hands/internal/loop 0.4s"
	if !strings.Contains(msgs[2].Content, want) {
		t.Errorf("line = %q", msgs[2].Content)
	}
}

func TestTranscriptElideReplyKeepsFinalAndShortArgs(t *testing.T) {
	long := strings.Repeat("y", 2000)
	got := elideReply(strings.Repeat("p", 900) + "\n" + `{"calls":[{"id":"c1","tool":"local_write","args":{"path":"a.go","content":"` + long + `"}}],"final":null}`)
	if strings.Contains(got, long) || !strings.Contains(got, "[2000 bytes — elided by hermes-hands]") || !strings.Contains(got, `"path":"a.go"`) {
		t.Errorf("long arg not elided: %.300s", got)
	}
	if strings.Count(got, "p") > 300 {
		t.Errorf("prose not clipped")
	}
	if got := elideReply(`{"calls":[],"final":"` + long + `"}`); !strings.Contains(got, long) {
		t.Errorf("the final must stay whole")
	}
}

func TestTranscriptCollapsedTurn(t *testing.T) {
	evs := append(roundTurn(1, 1, strings.Repeat("x", 5000)), roundTurn(2, 8, "ok")...)
	msgs := Transcript(evs, TranscriptOpts{Upto: 14, Collapse: 1, Output: func(int) string { return "" }})
	if msgs[0].Content != "[operator] why is CI red?" || msgs[1].Role != "assistant" {
		t.Fatalf("collapsed turn = %+v", msgs[:2])
	}
	if !strings.Contains(msgs[1].Content, "calls #1–#1") || !strings.HasSuffix(msgs[1].Content, "the lockfile is missing") {
		t.Errorf("collapsed answer = %q", msgs[1].Content)
	}
	if got := roles(msgs); got != "user assistant user assistant user assistant" {
		t.Errorf("roles = %s", got)
	}
}

// An older log has no replies: the envelope is rebuilt from the calls, the
// answer is the last reply.
func TestTranscriptOlderLogWithoutReplies(t *testing.T) {
	ok := true
	evs := []Event{
		{Seq: 1, Turn: 1, Kind: KindUser, Text: "ls"},
		{Seq: 2, Turn: 1, Kind: KindCall, Ref: 1, ID: "c1", Tool: "local_shell", Args: json.RawMessage(`{"cmd":"ls"}`)},
		{Seq: 3, Turn: 1, Kind: KindResult, Ref: 1, Exit: Int(0), Bytes: 4},
		{Seq: 4, Turn: 1, Kind: KindAnswer, Text: "two files", OK: &ok},
	}
	msgs := Transcript(evs, TranscriptOpts{Output: func(int) string { return "a\nb\n" }})
	if got := roles(msgs); got != "user assistant user assistant" {
		t.Fatalf("roles = %s", got)
	}
	if !strings.Contains(msgs[1].Content, `"tool":"local_shell"`) || msgs[3].Content != "two files" {
		t.Errorf("rebuilt = %+v", msgs)
	}
}

// A turn stopped by hermes-hands (BLOCKED) is said by hermes-hands, not put
// in the model's mouth; the next operator message merges into that note.
func TestTranscriptStoppedTurn(t *testing.T) {
	no := false
	evs := []Event{
		{Seq: 1, Turn: 1, Kind: KindUser, Text: "fix it"},
		{Seq: 2, Turn: 1, Kind: KindReply, Text: `{"calls":[{"id":"c1","tool":"local_shell","args":{"cmd":"make"}}],"final":null}`},
		{Seq: 3, Turn: 1, Kind: KindAnswer, Text: "BLOCKED: Hermes still requesting data after 25 rounds.", OK: &no},
		{Seq: 4, Turn: 2, Kind: KindUser, Text: "continue"},
	}
	msgs := Transcript(evs, TranscriptOpts{})
	if got := roles(msgs); got != "user assistant user" {
		t.Fatalf("roles = %s", got)
	}
	if !strings.HasPrefix(msgs[2].Content, "[hermes-hands: the turn stopped — BLOCKED") || !strings.HasSuffix(msgs[2].Content, "[operator] continue") {
		t.Errorf("stopped turn = %q", msgs[2].Content)
	}
}

func TestLastCompact(t *testing.T) {
	if _, ok := LastCompact([]Event{{Kind: KindUser}}); ok {
		t.Error("no compaction yet")
	}
	c, ok := LastCompact([]Event{{Kind: KindCompact, Upto: 3}, {Kind: KindUser}, {Kind: KindCompact, Upto: 9}})
	if !ok || c.Upto != 9 {
		t.Errorf("got %+v %v", c, ok)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
