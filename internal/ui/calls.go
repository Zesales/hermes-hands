package ui

import (
	"encoding/json"
	"fmt"
	"hermes-hands/internal/excerpt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CallStart is the line a hands call gets BEFORE it runs — so the operator
// always sees what is running right now, not only once it finished:
//
//	#14 shell  make test 2>&1 | tail -10
//	           …continuation lines of a multi-line command (at most 2)
//
// head / more come from Summarize. The #ref is session-wide: it is also what
// Hermes sees in its results and what `/out <ref>` takes.
func (u *UI) CallStart(ref int, tool, head string, more []string) {
	u.sync(func() {
		fmt.Fprintf(u.w, " %s%s#%d%s %s%s%s  %s\n", u.cB, u.cAcc, ref, u.cR, u.cB, tool, u.cR, head)
		pad := strings.Repeat(" ", len(strconv.Itoa(ref))+3+len(tool)+2)
		for _, m := range more {
			fmt.Fprintf(u.w, "%s%s%s%s\n", pad, u.cDim, m, u.cR)
		}
	})
}

// CallDone is a call's outcome line plus a short excerpt of its output:
//
//	#14 ✓ exit 0 · 1m12s · 38 lines
//	    │ ok   …/internal/loop   0.036s
//	    │ … 35 lines — /out 14
//	    │ ok   …/internal/ui     1.914s
//
// read_file shows no excerpt (the operator has the file); the full output of
// any call stays available through /out.
func (u *UI) CallDone(ref int, tool string, exit int, dur time.Duration, out string) {
	lines := excerpt.Lines(out)
	mark, col := "✓", u.cOK
	if exit != 0 {
		mark, col = "✗", u.cBad
	}
	status := "exit " + strconv.Itoa(exit)
	switch exit {
	case 124:
		status += " (timed out / shell reset)"
	case 125:
		status += " (declined)"
	case 127:
		status += " (command not found)"
	}
	info := []string{status}
	if dur >= time.Second {
		info = append(info, humanDur(dur))
	}
	if len(lines) == 0 {
		info = append(info, "no output")
	} else {
		info = append(info, nLines(len(lines)))
	}
	var rows []excerpt.Row
	if tool != "local_read" && tool != "read_file" {
		rows = excerpt.Pick(lines, exit)
	}
	width := u.cols() - 8
	u.sync(func() {
		fmt.Fprintf(u.w, " %s%s#%d%s %s%s%s %s%s%s\n", u.cB, u.cAcc, ref, u.cR,
			col, mark, u.cR, u.cDim, strings.Join(info, " · "), u.cR)
		for _, r := range rows {
			if r.Gap > 0 {
				fmt.Fprintf(u.w, "     %s│ … %s — /out %d%s\n", u.cDim, nLines(r.Gap), ref, u.cR)
				continue
			}
			fmt.Fprintf(u.w, "     %s│%s %s\n", u.cDim, u.cR, clipWidth(r.Text, width))
		}
	})
}

// HermesTool reports Hermes reaching for one of its OWN server-side tools.
// action "" is a thinking-side tool (web search, memory, cron) and "server"
// an exec / file tool doing server-side work — both dim notes; "steered"
// means the call was aimed at the operator's machine and the run was turned
// back to the hands, "stopped" a mistake past the run's steers for it, or one
// whose steer the server refused (hermes-hands stopped the run and asks
// again), "repeat" that when the server refused the stop too; "approved" /
// "denied" answer an approval request.
func (u *UI) HermesTool(tool, preview, action string) {
	preview = clipWidth(strings.Join(strings.Fields(preview), " "), u.cols()-30)
	u.sync(func() {
		switch action {
		case "steered":
			fmt.Fprintf(u.w, " %s⚠ hermes-sandbox%s %s%s%s  %s\n     %s↳ not your machine — steered back to the hands%s\n",
				u.cBad, u.cR, u.cB, tool, u.cR, preview, u.cDim, u.cR)
		case "stopped":
			fmt.Fprintf(u.w, " %s⚠ hermes-sandbox%s %s%s%s  %s\n     %s↳ run stopped — hermes-hands asks once more, with a correction%s\n",
				u.cBad, u.cR, u.cB, tool, u.cR, preview, u.cDim, u.cR)
		case "repeat":
			fmt.Fprintf(u.w, " %s⚠ hermes-sandbox%s %s%s%s  %s\n     %s↳ again — this run was steered for it already; not again%s\n",
				u.cBad, u.cR, u.cB, tool, u.cR, preview, u.cDim, u.cR)
		case "denied":
			fmt.Fprintf(u.w, " %s⚠ hermes-sandbox%s approval request denied  %s\n", u.cBad, u.cR, preview)
		case "approved":
			fmt.Fprintf(u.w, " %s✓ hermes-sandbox%s approval granted  %s\n", u.cOK, u.cR, preview)
		case "server":
			fmt.Fprintf(u.w, " %s· hermes server-sandbox %s  %s%s\n", u.cDim, tool, preview, u.cR)
		default:
			fmt.Fprintf(u.w, " %s· hermes %s  %s%s\n", u.cDim, tool, preview, u.cR)
		}
	})
}

// Approval re-draws an answered approval prompt from the event log, the way
// the prompt itself drew it live (internal/prompt for the hands' calls,
// askServerApproval for Hermes' own): the summary, its detail, the choices
// and the answer given. Hermes' server prompts answer once | session | deny.
func (u *UI) Approval(summary, detail, answer string) {
	key := map[string]string{"yes": "y", "no": "n", "all": "a", "quit": "q", "once": "o", "session": "s", "deny": "n"}[answer]
	u.sync(func() {
		fmt.Fprintf(u.w, "\n  ⚠  %s\n", summary)
		switch answer {
		case "once", "session", "deny":
			fmt.Fprintf(u.w, "      %s\n  [o]nce  [s]ession  [n]o > %s\n", detail, key)
		default:
			if detail != "" {
				io.WriteString(u.w, indentLines(detail, "      "))
			}
			fmt.Fprintf(u.w, "  [y]es  [n]o  [a]ll  [q]uit turn > %s\n", key)
		}
	})
}

// Note is one dim line inside a turn (a capped round budget, a cancel, …).
func (u *UI) Note(msg string) {
	u.sync(func() { fmt.Fprintf(u.w, " %s%s%s\n", u.cDim, msg, u.cR) })
}

// Divider is a dim rule with a label — where a session's chat was compacted:
// above it the whole chat, below it what Hermes reads from then on.
//
//	──── compacted #1 · ~98k → ~31k tokens · … · chat-checkpoints/0001.json ────
func (u *UI) Divider(label string) {
	u.sync(func() {
		fill := max(u.cols()-utf8.RuneCountInString(label)-6, 4)
		fmt.Fprintf(u.w, "%s──── %s %s%s\n", u.cDim, label, strings.Repeat("─", fill), u.cR)
	})
}

// SetActivity names what the spinner line is waiting on ("#14 shell"), with
// its own elapsed counter; "" goes back to the plain "working".
func (u *UI) SetActivity(label string) {
	u.mu.Lock()
	u.activity = label
	u.activitySince = time.Now()
	u.mu.Unlock()
}

// SetBudget puts where the turn stands against the operator's budget on the
// spinner line (BudgetLabel); "" takes it off.
func (u *UI) SetBudget(label string) {
	u.mu.Lock()
	u.budget = label
	u.mu.Unlock()
}

// BudgetLabel is "round 3/25 · calls 12/100" — a max of 0 (no limit) drops
// its "/max".
func BudgetLabel(round, maxRounds, calls, maxCalls int) string {
	of := func(n, limit int) string {
		if limit > 0 {
			return strconv.Itoa(n) + "/" + strconv.Itoa(limit)
		}
		return strconv.Itoa(n)
	}
	return "round " + of(round, maxRounds) + " · calls " + of(calls, maxCalls)
}

// ContextLabel is " · ctx 48k/131k" for the working line: the estimated
// prompt of the request in flight against the model's window — "" while
// nothing is known yet.
func ContextLabel(tokens, window int) string {
	if tokens <= 0 {
		return ""
	}
	if window <= 0 {
		return " · ctx " + kilo(tokens)
	}
	return " · ctx " + kilo(tokens) + "/" + kilo(window)
}

// kilo is "48k" / "950".
func kilo(n int) string {
	if n >= 1000 {
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return strconv.Itoa(n)
}

// plural is "1 round" / "3 rounds".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// Summarize turns a call's args into the CallStart head line plus up to two
// continuation lines: a shell command's text (and a non-default timeout), a
// file path (and a write's size). Unknown tools show their compact args.
func Summarize(tool string, args json.RawMessage) (head string, more []string) {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(args, &m)
	str := func(k string) string {
		var s string
		if raw, ok := m[k]; ok && json.Unmarshal(raw, &s) != nil {
			s = strings.TrimSpace(string(raw))
		}
		return s
	}
	switch tool {
	case "local_shell", "shell", "run", "bash", "exec", "terminal":
		lines := strings.Split(strings.TrimRight(str("cmd"), "\n"), "\n")
		head = lines[0]
		rest := lines[1:]
		if len(rest) > 2 {
			more = append(rest[:2:2], "… +"+strconv.Itoa(len(rest)-2)+" lines")
		} else {
			more = rest
		}
		if t := str("timeout"); t != "" && t != "0" {
			head += "   (timeout " + t + "s)"
		}
	case "local_read", "read_file":
		head = str("path")
	case "local_write", "write_file":
		n := strings.Count(str("content"), "\n")
		head = fmt.Sprintf("%s   (%d lines)", str("path"), n+1)
	case "local_edit", "edit_file":
		head = str("path")
	case "local_output":
		head = "output of #" + strings.TrimPrefix(str("ref"), "#")
		if o := str("offset"); o != "" && o != "0" {
			head += " from byte " + o
		}
	default:
		head = clipWidth(string(args), 100)
	}
	return head, more
}

func nLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

// clipWidth shortens s to at most w runes, marking the cut with "…".
func clipWidth(s string, w int) string {
	if w < 8 {
		w = 8
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

// OutputBlock prints a stored call output in full (`/out <ref>`).
func (u *UI) OutputBlock(ref int, out string) {
	u.sync(func() {
		fmt.Fprintf(u.w, " %s%s#%d%s %soutput (%d bytes)%s\n", u.cB, u.cAcc, ref, u.cR, u.cDim, len(out), u.cR)
		io.WriteString(u.w, indentLines(strings.TrimRight(out, "\n"), "     "))
	})
}
