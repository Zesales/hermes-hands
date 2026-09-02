package main

import _ "embed"

// builtinInstructions is the per-run `instructions` block sent to Hermes when no
// override file is present. Source of truth: share/instructions.md (embedded at
// build time, so the binary is self-contained). Was $HH_INSTRUCTIONS_BUILTIN in
// the bash bundle.
//
//go:embed share/instructions.md
var builtinInstructions string
