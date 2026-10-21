package loop

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"hermes-hands/internal/api"
)

// @path attachments, as in Claude Code: a file the operator names in a
// message with @path — or drops into the terminal, which pastes an absolute
// image path — goes along with it, read-only, also from outside the working
// directory: the operator named it. An image as an image (the model sees it
// in this turn's first run; hermes-agent keeps only "[screenshot]" of it), a
// text file as its content. A name that is no readable file stays plain text.

const (
	maxImageBytes = 5 << 20   // per image — what the common vision APIs take
	maxFileBytes  = 100 << 10 // per text file, inline
)

var (
	// @path, @"path with spaces", @'path'
	atRe = regexp.MustCompile(`(?:^|\s)@(?:"([^"]+)"|'([^']+)'|(\S+))`)
	// a dropped image: an absolute or ~/ path, maybe quoted or with escaped
	// spaces, ending in an image extension
	dropRe = regexp.MustCompile(`(?i)(?:^|\s)(?:"((?:/|~/)[^"]+\.(?:png|jpe?g|gif|webp))"|'((?:/|~/)[^']+\.(?:png|jpe?g|gif|webp))'|((?:/|~/)(?:\\ |\S)+\.(?:png|jpe?g|gif|webp)))`)

	imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}
)

// attach reads the files msg names: it returns msg with each text file's
// content appended, and the images to send along. What it attached — or why
// not — the operator sees as a note.
func (l *Loop) attach(msg string) (string, []api.Image) {
	var images []api.Image
	var files []string
	seen := map[string]bool{}
	add := func(raw string, imagesOnly bool) {
		p, shown, fi := l.attachable(raw)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		name := filepath.Base(p)
		mime := sniff(p)
		switch {
		case imageTypes[mime]:
			if _, ok := l.API.(ImageAsker); !ok {
				l.noteOperator("@" + name + ": not attached — images cannot be sent from here")
				return
			}
			if fi.Size() > maxImageBytes {
				l.noteOperator(fmt.Sprintf("@%s: not attached — %s, over the %s an image may have", name, size(fi.Size()), size(maxImageBytes)))
				return
			}
			b, err := os.ReadFile(p)
			if err != nil {
				l.noteOperator("@" + name + ": not attached — " + err.Error())
				return
			}
			images = append(images, api.Image{Name: name, Mime: mime, Data: b})
			l.noteOperator(fmt.Sprintf("attached image #%d · %s · %s", len(images), name, size(int64(len(b)))))
		case imagesOnly: // a dropped path that is no image stays plain text
		default:
			text, cut, ok := readText(p)
			if !ok {
				l.noteOperator("@" + name + ": not attached — neither text nor an image (png, jpeg, gif, webp)")
				return
			}
			block := fmt.Sprintf("[@%s — attached by the operator, %s]\n%s", shown, size(fi.Size()), l.scrub(text))
			if cut {
				block += fmt.Sprintf("\n[… cut at %s of %s]", size(maxFileBytes), size(fi.Size()))
			}
			files = append(files, block+"\n[end of @"+shown+"]")
			l.noteOperator(fmt.Sprintf("attached file · %s · %s", name, size(fi.Size())))
		}
	}
	for _, m := range atRe.FindAllStringSubmatch(msg, -1) {
		add(firstGroup(m), false)
	}
	for _, m := range dropRe.FindAllStringSubmatch(msg, -1) {
		add(strings.ReplaceAll(firstGroup(m), `\ `, " "), true)
	}
	if len(files) > 0 {
		msg += "\n\n" + strings.Join(files, "\n\n")
	}
	return msg, images
}

// attachable resolves a named path — ~/ the home directory, a relative path
// the working directory — to a regular file, or "" when it names none; shown
// is the name as named, without a sentence's trailing punctuation.
func (l *Loop) attachable(raw string) (p, shown string, fi os.FileInfo) {
	for _, cand := range []string{raw, strings.TrimRight(raw, ".,;:!?)")} {
		p = cand
		switch {
		case strings.HasPrefix(p, "~/"):
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			p = filepath.Join(home, p[2:])
		case !filepath.IsAbs(p):
			p = filepath.Join(l.RepoRoot, p)
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, cand, st
		}
	}
	return "", "", nil
}

// sniff is a file's media type from its first bytes.
func sniff(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return http.DetectContentType(head[:n])
}

// readText reads up to maxFileBytes of a text file: ok false when it is not
// text (NUL bytes, invalid UTF-8), cut when there was more.
func readText(p string) (text string, cut, ok bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", false, false
	}
	if cut = len(b) > maxFileBytes; cut {
		b = b[:maxFileBytes]
		for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ { // a rune cut in half at the limit
			b = b[:len(b)-1]
		}
	}
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return "", false, false
	}
	return string(b), cut, true
}

func firstGroup(m []string) string {
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// size is "512 B" / "240 KB" / "1.4 MB".
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%d KB", (n+512)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
