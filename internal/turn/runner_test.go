package turn

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/dispatch"
	"github.com/Zesales/hermes-hands/internal/hermesmock"
	"github.com/Zesales/hermes-hands/internal/loop"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/session"
)

// fakeFront records what a turn showed.
type fakeFront struct {
	kinds   []string
	preview strings.Builder
	open    bool
}

func (f *fakeFront) Show(ev session.Event) { f.kinds = append(f.kinds, ev.Kind) }
func (f *fakeFront) Preview(s string)      { f.preview.WriteString(s); f.open = true }
func (f *fakeFront) Quiet()                {}
func (f *fakeFront) EndPreview() bool {
	was := f.open
	f.open = false
	return was
}

func newTestRunner(t *testing.T, srv *httptest.Server) (*Runner, *session.Store) {
	t.Helper()
	client := &api.Client{
		HTTP: api.NewHTTPClient(2*time.Second, 5*time.Second), BaseURL: srv.URL, Key: "k",
		AllowHTTP: true, Retries: 1, PollInterval: time.Millisecond, RunTimeout: 5 * time.Second,
		Warnf: func(string, ...any) {},
	}
	if _, err := client.Server().Check(context.Background()); err != nil { // reads the caps: stream or poll
		t.Fatal(err)
	}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rec, err := store.Resolve("new", dir)
	if err != nil {
		t.Fatal(err)
	}
	lp := &loop.Loop{API: client.Runs(), MaxRounds: 5,
		Dispatch: &dispatch.Dispatcher{RepoRoot: dir, Approver: prompt.AutoApprover{}, MaxOutput: 20000, RunTimeout: 5 * time.Second}}
	r := &Runner{Client: client, Loop: lp, Store: store, Shell: nopShell{}, Timeout: 30 * time.Second, Interval: time.Minute}
	log, _ := store.OpenLog(rec)
	r.Use(log)
	return r, store
}

type nopShell struct{}

func (nopShell) Kill() {}

// A turn shows the operator's message first and the footer last; the answer
// in between only when the live preview has not already shown it.
func TestRunnerShowsTheTurnInOrder(t *testing.T) {
	for _, tc := range []struct {
		name        string
		srv         *httptest.Server
		wantKinds   string
		wantPreview string
	}{
		{"streamed: the preview stands in for the answer", hermesmock.NewStream("plain"),
			"user turn_end", "plain answer from the mock brain."},
		{"polled: the answer is shown", hermesmock.New("plain"),
			"user answer turn_end", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer tc.srv.Close()
			r, store := newTestRunner(t, tc.srv)
			fe := &fakeFront{}
			res := r.Run("hello", fe)

			if got := strings.Join(fe.kinds, " "); got != tc.wantKinds {
				t.Errorf("shown = %q, want %q", got, tc.wantKinds)
			}
			if fe.preview.String() != tc.wantPreview {
				t.Errorf("preview = %q, want %q", fe.preview.String(), tc.wantPreview)
			}
			if !res.Out.OK || res.Out.Answer != "plain answer from the mock brain." || res.End.Outcome != "" {
				t.Errorf("result = %+v", res)
			}
			// the log has every event, shown or not
			evs, _ := store.LoadEvents(r.Log().Rec)
			var kinds []string
			for _, e := range evs {
				kinds = append(kinds, e.Kind)
			}
			if strings.Join(kinds, " ") != "user answer turn_end" {
				t.Errorf("recorded = %v", kinds)
			}
			if r.Client.OnDelta != nil || r.Loop.Emit != nil {
				t.Errorf("hooks still wired after the turn")
			}
		})
	}
}

// Cancel between turns does nothing — no turn context, no run to stop.
func TestRunnerCancelBetweenTurns(t *testing.T) {
	srv := hermesmock.New("plain")
	defer srv.Close()
	r, _ := newTestRunner(t, srv)
	r.Cancel()
	if res := r.Run("hello", &fakeFront{}); res.End.Outcome != "" {
		t.Errorf("a Cancel before the turn leaked into it: %+v", res.End)
	}
}
