package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermes-hands/internal/api"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/session"
)

// imageAsker is a scriptAsker that takes images (api.Runs.AskImages) and
// records what each request carried.
type imageAsker struct {
	scriptAsker
	images [][]api.Image
}

func (a *imageAsker) AskImages(ctx context.Context, msg, sess, skey string, _ []api.Message, images []api.Image) (api.AskResult, error) {
	a.images = append(a.images, images)
	return a.Ask(ctx, msg, sess, skey)
}

const pngHead = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

// @path attaches, as in Claude Code: an image as an image, a text file as
// its content; a name that is no file stays text, a binary file is refused
// with a note, and a dropped absolute image path counts as an attachment
func TestAttach(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir() // outside the working directory: the operator named it
	os.WriteFile(filepath.Join(dir, "shot.png"), []byte(pngHead), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("api_key: supersecretvalue\nline two\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("a\x00b"), 0o644)
	dropped := filepath.Join(other, "my shot.png")
	os.WriteFile(dropped, []byte(pngHead), 0o644)

	a := &imageAsker{}
	var notes []string
	l := &Loop{API: a, RepoRoot: dir, Scrub: func(s string) string { return strings.ReplaceAll(s, "supersecretvalue", "[redacted]") },
		Emit: func(ev session.Event) {
			if ev.Kind == session.KindNote {
				notes = append(notes, ev.Text)
			}
		}}
	msg := "look at @shot.png and @notes.txt, ask @team, skip @blob.bin and '" + dropped + "'"
	text, images := l.attach(msg)

	if len(images) != 2 || images[0].Name != "shot.png" || images[0].Mime != "image/png" || images[1].Name != "my shot.png" {
		t.Errorf("images = %+v", images)
	}
	if !strings.HasPrefix(text, msg) || !strings.Contains(text, "[@notes.txt — attached by the operator, ") ||
		!strings.Contains(text, "line two") || !strings.Contains(text, "[end of @notes.txt]") {
		t.Errorf("text = %q", text)
	}
	if strings.Contains(text, "supersecretvalue") || strings.Contains(text, "shot.png —") || strings.Contains(text, "@team —") {
		t.Errorf("text must carry only the scrubbed text file: %q", text)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"attached image #1 · shot.png", "attached file · notes.txt", "@blob.bin: not attached — neither text nor an image", "attached image #2 · my shot.png"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes miss %q:\n%s", want, joined)
		}
	}
}

// the images go with the turn's first run only; the rounds after it restate
// the message without the attached files
func TestAttachedImagesGoOnce(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "shot.png"), []byte(pngHead), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the notes"), 0o644)
	a := &imageAsker{scriptAsker: scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"read_file","args":{"path":"notes.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}}
	d := realDisp(t, prompt.AutoApprover{})
	d.RepoRoot = dir
	l := &Loop{API: a, Dispatch: d, MaxRounds: 8, RepoRoot: dir}
	if out := l.Run(context.Background(), "what is this? @shot.png @notes.txt", rec(), func(string, string, int) {}); !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if len(a.images) != 1 || len(a.images[0]) != 1 {
		t.Errorf("AskImages calls = %d, want 1 with the image", len(a.images))
	}
	if !strings.Contains(a.msgs[0], "[@notes.txt — attached by the operator") {
		t.Errorf("round 1 must carry the file: %q", a.msgs[0])
	}
	if len(a.msgs) < 2 || strings.Contains(a.msgs[1], "[@notes.txt — attached by the operator") ||
		!strings.Contains(a.msgs[1], "what is this? @shot.png @notes.txt") {
		t.Errorf("round 2 restates the message, not the file: %v", a.msgs[1:])
	}
}
