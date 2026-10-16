package app

import (
	"fmt"
	"os"
)

// Warnf and Logf are hermes-hands' own lines on stderr ("hermes-hands: …").
func Warnf(f string, a ...any) { fmt.Fprintf(os.Stderr, "hermes-hands: WARNING: "+f+"\n", a...) }
func Logf(f string, a ...any)  { fmt.Fprintf(os.Stderr, "hermes-hands: "+f+"\n", a...) }
