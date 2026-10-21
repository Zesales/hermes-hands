package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// a message with images goes as one user message of parts: the text, then
// each image as an image_url with a data: URL — without images the input
// stays the plain string
func TestAskImages_SendsParts(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			writeTestJSON(w, 200, `{"run_id":"run_1","status":"started"}`)
			return
		}
		writeTestJSON(w, 200, `{"status":"completed","output":"seen"}`)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	img := Image{Name: "shot.png", Mime: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}
	if res, err := c.Runs().AskImages(context.Background(), "what is on it?", "", "", nil, []Image{img}); err != nil || res.Text != "seen" {
		t.Fatalf("AskImages = %+v, %v", res, err)
	}
	if _, err := c.Runs().Ask(context.Background(), "plain", "", ""); err != nil {
		t.Fatal(err)
	}
	var withImage struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &withImage); err != nil {
		t.Fatalf("body with an image: %v\n%s", err, bodies[0])
	}
	in := withImage.Input
	if len(in) != 1 || in[0].Role != "user" || len(in[0].Content) != 2 ||
		in[0].Content[0].Type != "text" || in[0].Content[0].Text != "what is on it?" ||
		in[0].Content[1].Type != "image_url" || in[0].Content[1].ImageURL.URL != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("input with an image = %+v", in)
	}
	if !strings.Contains(bodies[1], `"input":"plain"`) {
		t.Errorf("input without images must stay the string: %s", bodies[1])
	}
}

// a 4xx for the images is the images' fault: reported as such, and the
// session id is kept — no second POST without it
func TestAskImages_RefusedKeepsTheSession(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		writeTestJSON(w, 400, `{"error":{"message":"Image inputs must use http(s) URLs or data:image/... URLs.","code":"invalid_image_url"}}`)
	}))
	defer srv.Close()

	img := Image{Name: "x.png", Mime: "image/png", Data: []byte("x")}
	_, err := testClient(srv.URL).Runs().AskImages(context.Background(), "hi", "hh-sess", "", nil, []Image{img})
	if err == nil || !strings.HasPrefix(err.Error(), "the server refused the attached image(s) - HTTP 400") {
		t.Fatalf("err = %v", err)
	}
	if posts != 1 {
		t.Errorf("posts = %d, want 1 (no retry without the session id)", posts)
	}
}
