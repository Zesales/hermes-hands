package session

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// FitRound fits one round's call outputs into limit bytes together — the
// round output budget (HERMES_HANDS_MAX_ROUND_OUTPUT, 0 = no limit). Every
// output keeps a fair share: the small ones whole, what they leave is split
// over the big ones, which keep their head and their tail (where a build or a
// test says how it ended) around a marker naming the cut bytes. The full
// output stays in the local store, so the marker tells Hermes how to read the
// cut part back (local_output) instead of re-running the command. outs is not
// modified; refs[i] is the #ref of outs[i]. The live round (internal/loop)
// and a transcript rebuilt from the log (Transcript) cut the same way.
func FitRound(outs []string, refs []int, limit int) []string {
	fitted := append([]string(nil), outs...)
	total := 0
	for _, o := range outs {
		total += len(o)
	}
	if limit <= 0 || total <= limit || len(outs) == 0 {
		return fitted
	}
	// water-filling: hand out the budget smallest first; whoever needs less
	// than an even share of what is left keeps everything
	idx := make([]int, len(outs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return len(outs[idx[a]]) < len(outs[idx[b]]) })
	left := limit
	for n, i := range idx {
		share := left / (len(idx) - n)
		if len(outs[i]) <= share {
			left -= len(outs[i])
			continue
		}
		fitted[i] = cutMiddle(outs[i], refs[i], share)
		left -= min(share, len(fitted[i]))
	}
	return fitted
}

// minKeep is the least of an output a cut keeps, however tight the round.
const minKeep = 600

// cutMiddle keeps the head (60%) and the tail (40%) of out in about keep
// bytes and puts a marker in between that says which bytes are missing and
// how to read them back.
func cutMiddle(out string, ref, keep int) string {
	keep = max(keep-260, minKeep) // room for the marker
	if keep >= len(out) {
		return out
	}
	head := runeStart(out, keep*3/5)
	tail := runeStart(out, len(out)-(keep-head))
	return out[:head] + fmt.Sprintf("\n[… bytes %d–%d of %d cut by hermes-hands — this round's results share a byte budget. "+
		`The full output stays local: local_output {"ref":%d,"offset":%d,"limit":%d} reads the cut part; or narrow the command (rg, sed -n, head).]`+"\n",
		head, tail, len(out), ref, head, tail-head) + out[tail:]
}

// runeStart moves i back to the start of the UTF-8 sequence it falls in.
func runeStart(s string, i int) int {
	i = max(min(i, len(s)), 0)
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
