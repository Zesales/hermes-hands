package api

import (
	"context"
	"encoding/base64"
	"strings"
)

// Image is one image the operator attached to a message (@path): its media
// type and bytes. It goes along as an image_url part with a data: URL — the
// shape hermes-agent's multimodal content takes. hermes-agent keeps only
// "[screenshot]" of it in the session afterwards: the model sees it in the
// run it is sent with.
type Image struct {
	Name string // the file's base name, for notes
	Mime string // image/png | image/jpeg | image/gif | image/webp
	Data []byte
}

func (im Image) dataURL() string {
	return "data:" + im.Mime + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
}

// inputMessage / contentPart: a run's input as one user message of text and
// image parts — for a message with images; without, input is the string.
type inputMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// runInput is the POST /v1/runs input: msg as is, or — with images — one user
// message carrying msg and the images as parts.
func runInput(msg string, images []Image) any {
	if len(images) == 0 {
		return msg
	}
	parts := []contentPart{{Type: "text", Text: msg}}
	for _, im := range images {
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: im.dataURL()}})
	}
	return []inputMessage{{Role: "user", Content: parts}}
}

// imagesRefused: a 4xx the server gave for the images (an invalid image, a
// model without vision) — no reason to drop the session id and try again.
func imagesRefused(images []Image, body []byte) bool {
	return len(images) > 0 && strings.Contains(strings.ToLower(string(body)), "image")
}

// AskImages is AskWith with images along with msg (the operator's @path
// attachments).
func (r Runs) AskImages(ctx context.Context, msg, sess, skey string, history []Message, images []Image) (AskResult, error) {
	return r.askRun(ctx, msg, sess, skey, history, images)
}
