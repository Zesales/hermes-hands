package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/session"
)

// maxSessionLookups bounds the per-session GETs of a verbose list: a local
// session missing from the server's latest-100 listing is looked up by id.
const maxSessionLookups = 20

// SessionList renders `--session-list` / `/sessions`: this machine's sessions
// only. verbose (`--session-list-verbose`, `/sessions -v`) adds, under each
// one, its hermes-agent counterpart — a local-vs-remote view for debugging.
func SessionList(store *session.Store, client *api.Client, verbose bool) string {
	recs, _ := store.List()
	if !verbose || client == nil {
		return session.FormatList(recs)
	}
	ctx := context.Background()
	ss, err := client.ListSessions(ctx)
	if err != nil {
		return session.FormatList(recs) + "\nhermes-agent: " + err.Error() + "\n"
	}
	info := func(id string) (api.SrvSession, error) { return client.SessionInfo(ctx, id) }
	return formatSessionCompare(recs, ss, info)
}

// formatSessionCompare is the verbose list: the local table with each
// session's hermes-agent session under it, then the hermes-hands sessions the
// server still holds that have no local record any more (retention deleted
// them, or another machine ran them). Everything else on the server — cron
// jobs, other clients — is only counted. info looks up a session the listing
// didn't include (nil: no lookups).
func formatSessionCompare(recs []session.Record, ss []api.SrvSession, info func(id string) (api.SrvSession, error)) string {
	byID := make(map[string]api.SrvSession, len(ss))
	for _, s := range ss {
		byID[s.ID] = s
	}
	matched := map[string]bool{}
	lookups := 0
	under := func(i int) string {
		const pre = "   ↳ hermes-agent  "
		r := recs[i]
		if r.HermesSessionID == "" || r.Turns == 0 {
			return pre + "— no turn sent yet\n"
		}
		s, ok := byID[r.HermesSessionID]
		if !ok {
			if info == nil || lookups >= maxSessionLookups {
				return pre + r.HermesSessionID + " · not in the server's latest " + strconv.Itoa(len(ss)) + " — not checked\n"
			}
			lookups++
			got, err := info(r.HermesSessionID)
			switch {
			case err == nil && got.ID != "":
				s, ok = got, true
			case err != nil && !strings.Contains(err.Error(), "HTTP 404"):
				return pre + r.HermesSessionID + " · not checked: " + err.Error() + "\n"
			}
		}
		if !ok {
			return pre + r.HermesSessionID + " · not on the server — the next turn sends the local history along\n"
		}
		matched[s.ID] = true
		return pre + srvDetail(s) + "\n"
	}
	var b strings.Builder
	b.WriteString(session.FormatListWith(recs, under))

	var orphans []api.SrvSession
	for _, s := range ss {
		if matched[s.ID] || !strings.HasPrefix(s.ID, "hh-") {
			continue
		}
		for _, c := range ss { // a split / fork the local session moved on to
			if c.Parent == s.ID && matched[c.ID] {
				s.Title += "  (continued as " + c.ID + ")"
				break
			}
		}
		orphans = append(orphans, s)
	}
	if len(orphans) > 0 {
		b.WriteString("\n" + formatSrvList(fmt.Sprintf("hermes-agent sessions of hermes-hands without a local session (%d):", len(orphans)), orphans))
	}
	if other := len(ss) - len(orphans) - countIn(ss, matched); other > 0 {
		fmt.Fprintf(&b, "\n+ %d other hermes-agent session(s) — cron jobs, other clients — not shown.\n", other)
	}
	return b.String()
}

// srvDetail is one server session in a line: id, messages, context tokens,
// model, last activity, lineage, ended.
func srvDetail(s api.SrvSession) string {
	parts := []string{s.ID, strconv.Itoa(s.Messages) + " msgs"}
	if s.Tokens > 0 {
		parts = append(parts, strconv.Itoa(s.Tokens)+" tok")
	}
	if s.Model != "" {
		parts = append(parts, s.Model)
	}
	if s.Updated != "" {
		parts = append(parts, "last "+s.Updated)
	}
	if s.Parent != "" {
		parts = append(parts, "continues "+s.Parent)
	}
	if s.Ended {
		parts = append(parts, "ended")
	}
	return strings.Join(parts, " · ")
}

func countIn(ss []api.SrvSession, set map[string]bool) int {
	n := 0
	for _, s := range ss {
		if set[s.ID] {
			n++
		}
	}
	return n
}

// formatSrvList renders server-side sessions (from GET /api/sessions) under
// a heading line.
func formatSrvList(head string, ss []api.SrvSession) string {
	var b strings.Builder
	b.WriteString(head + "\n")
	fmt.Fprintf(&b, "%-40s  %5s  %7s  %s\n", "SESSION", "MSGS", "TOKENS", "TITLE")
	for _, s := range ss {
		id := s.ID
		if len(id) > 40 {
			id = id[:39] + "…"
		}
		tok := "-"
		if s.Tokens > 0 {
			tok = strconv.Itoa(s.Tokens)
		}
		title := s.Title
		if s.Parent != "" {
			title += "  (from " + s.Parent + ")"
		}
		fmt.Fprintf(&b, "%-40s  %5d  %7s  %s\n", id, s.Messages, tok, title)
	}
	return b.String()
}
