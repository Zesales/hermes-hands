package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/session"
)

func TestFormatSessionCompare(t *testing.T) {
	recs := []session.Record{
		{ID: "hh_a", HermesSessionID: "hh-1-a", Turns: 2, Title: "listed"},
		{ID: "hh_b", HermesSessionID: "hh-1-b", Turns: 1, Title: "looked up"},
		{ID: "hh_c", HermesSessionID: "hh-1-c", Turns: 3, Title: "gone"},
		{ID: "hh_d", HermesSessionID: "hh-1-d", Turns: 0, Title: "never sent"},
		{ID: "hh_e", HermesSessionID: "hh-1-e2", Turns: 5, Title: "split"},
	}
	ss := []api.SrvSession{
		{ID: "hh-1-a", Messages: 6, Tokens: 57725, Title: "listed"},
		{ID: "hh-1-e", Messages: 40, Title: "split (old half)"},
		{ID: "hh-1-e2", Messages: 3, Parent: "hh-1-e"},
		{ID: "hh-9-orphan", Messages: 4, Tokens: 29614, Title: "retention removed it"},
		{ID: "cron_1", Messages: 10, Title: "TR Portfolio Update"},
		{ID: "7b0c57de-6897", Messages: 2, Title: "Next steps"},
	}
	var looked []string
	info := func(id string) (api.SrvSession, error) {
		looked = append(looked, id)
		if id == "hh-1-b" {
			return api.SrvSession{ID: id, Messages: 2}, nil
		}
		return api.SrvSession{}, fmt.Errorf("GET /api/sessions/%s -> HTTP 404", id)
	}
	out := formatSessionCompare(recs, ss, info)
	for _, want := range []string{
		"   ↳ hermes-agent  hh-1-a · 6 msgs · 57725 tok\n",
		"   ↳ hermes-agent  hh-1-b · 2 msgs\n",
		"   ↳ hermes-agent  hh-1-c · not on the server",
		"   ↳ hermes-agent  — no turn sent yet\n",
		"   ↳ hermes-agent  hh-1-e2 · 3 msgs · continues hh-1-e\n",
		"hermes-agent sessions of hermes-hands without a local session (2):",
		"retention removed it",
		"split (old half)  (continued as hh-1-e2)",
		"+ 2 other hermes-agent session(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TR Portfolio") || strings.Contains(out, "Next steps") {
		t.Errorf("non-hermes-hands sessions must only be counted:\n%s", out)
	}
	if strings.Join(looked, ",") != "hh-1-b,hh-1-c" {
		t.Errorf("lookups = %v, want only the two unlisted sessions that had turns", looked)
	}
	// the plain list never asks the server
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(SessionList(store, nil, false), "hermes-agent") {
		t.Errorf("plain list mentions hermes-agent")
	}
}
