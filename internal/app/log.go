package app

import (
	"fmt"
	"os"
)

// Warnf and Logf are hermes-hands' own lines on stderr ("hermes-hands: …").
func Warnf(f string, a ...any) { lines(fmt.Sprintf("hermes-hands: WARNING: "+f+"\n", a...)) }
func Logf(f string, a ...any)  { lines(fmt.Sprintf("hermes-hands: "+f+"\n", a...)) }

// lines writes them; New points it at the UI, so a warning that comes up
// while a turn runs lands above the dock, not inside it.
var lines = func(s string) { fmt.Fprint(os.Stderr, s) }
