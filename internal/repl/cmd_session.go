package repl

import (
	"context"
	"fmt"
	"strconv"

	"hermes-hands/internal/app"
	"hermes-hands/internal/session"
)

// session: no argument shows the open session's detail (local and, when the
// gateway answers, server numbers); an id switches to that session.
func (r *repl) session(arg string) {
	if arg == "" {
		fresh, _ := r.a.Store.Resolve(r.rec.ID, r.a.RepoRoot)
		if fresh == nil {
			fresh = r.rec
		}
		fmt.Fprint(r.conv, session.FormatDetail(fresh))
		if si, err := r.a.Client.Sessions().Info(context.Background(), fresh.HermesSessionID); err == nil {
			fmt.Fprintln(r.conv, "  — from hermes-agent —")
			if si.Messages > 0 {
				fmt.Fprintf(r.conv, "  messages             : %d\n", si.Messages)
			}
			if si.Tokens > 0 {
				fmt.Fprintf(r.conv, "  context tokens       : %d\n", si.Tokens)
			}
			if si.Parent != "" {
				fmt.Fprintf(r.conv, "  parent (pre-compact) : %s\n", si.Parent)
			}
			if si.Model != "" {
				fmt.Fprintf(r.conv, "  model                : %s\n", si.Model)
			}
			if si.Ended {
				fmt.Fprintln(r.conv, "  ended                : yes")
			}
		}
		fmt.Fprintln(r.conv)
		return
	}
	nr, err := r.a.Store.Resolve(arg, r.a.RepoRoot)
	if err != nil {
		fmt.Fprintf(r.conv, "  %v\n\n", err)
		return
	}
	r.a.UI.NewSessionNote(nr.ID)
	r.use(nr, true)
}

// sessions lists this machine's sessions; -v adds each one's hermes-agent
// session.
func (r *repl) sessions(arg string) {
	fmt.Fprint(r.conv, app.SessionList(r.a.Store, r.a.Client, arg == "-v" || arg == "--verbose"))
	fmt.Fprintln(r.conv)
}

func (r *repl) newSession(string) {
	nr, err := r.a.Store.Resolve("new", r.a.RepoRoot)
	if err != nil {
		fmt.Fprintf(r.conv, "hermes-hands: %v\n", err)
		return
	}
	r.a.UI.NewSessionNote(nr.ID)
	r.use(nr, false)
}

// fork branches the session on the server and switches to the branch.
func (r *repl) fork(string) {
	if r.rec.HermesSessionID == "" {
		fmt.Fprintln(r.conv, "  no hermes-agent session yet — run a turn first")
		fmt.Fprintln(r.conv)
		return
	}
	newID, err := r.a.Client.Sessions().Fork(context.Background(), r.rec.HermesSessionID)
	if err != nil {
		fmt.Fprintf(r.conv, "  BLOCKED: %v\n\n", err)
		return
	}
	nr, _ := r.a.Store.Resolve("new", r.a.RepoRoot)
	nr.HermesSessionID = newID
	_, _ = r.a.Store.SetTitleLocal(nr, "fork of "+r.rec.ID)
	r.use(nr, false)
	fmt.Fprintf(r.conv, "  forked → hermes-agent session %s (local %s)\n\n", newID, r.rec.ID)
}

// history replays this session, every turn; <n> its last n turns; <id>
// another session's — read-only, it never switches the REPL to it.
func (r *repl) history(arg string) {
	target, n := r.rec, 0
	if arg != "" {
		if k, err := strconv.Atoi(arg); err == nil && k > 0 {
			n = k
		} else if other, err := r.a.Store.Load(arg); err == nil {
			target = other
		} else {
			fmt.Fprintf(r.conv, "  no such session: %s\n\n", arg)
			return
		}
	}
	evs, err := r.a.Store.LoadEvents(target)
	if err != nil {
		app.Warnf("history: %v", err)
		return
	}
	r.a.UI.Replay(evs, n, target.ID, app.StoredOutput(r.a.Store, target))
	fmt.Fprintln(r.conv)
}

// out prints the stored full output of call #<n>.
func (r *repl) out(arg string) {
	u := r.a.UI
	ref, err := strconv.Atoi(arg)
	if err != nil || ref <= 0 {
		u.Note("usage: /out <n> — n is the #number of a call")
		u.Line("")
		return
	}
	out, err := r.a.Store.LoadOutput(r.rec, ref)
	if err != nil {
		u.Note(fmt.Sprintf("no stored output for #%d in this session", ref))
		u.Line("")
		return
	}
	u.OutputBlock(ref, out)
	u.Line("")
}
