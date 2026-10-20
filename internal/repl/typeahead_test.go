package repl

import (
	"os"
	"path/filepath"
	"testing"

	"hermes-hands/internal/loop"
	"hermes-hands/internal/ui"
)

// the keys of the type-ahead, without a terminal: Enter queues a line, a
// /command waits for the prompt, editing keys edit, escape sequences and a
// rune split across reads come through right
func TestTypeaheadKeys(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "ui"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	inbox := &loop.Inbox{}
	ta := &typeahead{ui: ui.New(f), inbox: inbox}

	rest := ta.feed([]byte("also chekc\x7f\x7fck the \x1b[Dtests\r"))
	rest = ta.feed(append(rest, []byte("/approve plan\rgrüß")...))
	rest = ta.feed(append(rest, []byte("e\xc3")...)) // the start of an "ü"
	if len(rest) != 1 {
		t.Fatalf("an incomplete rune must wait: rest = %q", rest)
	}
	rest = ta.feed(append(rest, []byte("\xbc\x17wort \x15bye")...))
	if len(rest) != 0 {
		t.Errorf("rest = %q", rest)
	}
	if got := inbox.Take(); len(got) != 1 || got[0] != "also check the tests" {
		t.Errorf("queued = %q", got)
	}
	if len(ta.cmds) != 1 || ta.cmds[0] != "/approve plan" {
		t.Errorf("commands = %q", ta.cmds)
	}
	if string(ta.buf) != "bye" {
		t.Errorf("being typed = %q (Ctrl-W and Ctrl-U clear)", string(ta.buf))
	}
	var none *typeahead // no terminal: every method a no-op
	none.hold()()
	if typed, cmds := none.finish(); typed != "" || cmds != nil {
		t.Error("a nil type-ahead has nothing")
	}
}
