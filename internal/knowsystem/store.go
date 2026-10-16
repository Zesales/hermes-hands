package knowsystem

import (
	"encoding/json"
	"os"
	"regexp"
)

var notFoundRe = regexp.MustCompile(`: ([A-Za-z0-9_][A-Za-z0-9_.+/-]*): command not found`)

// ObserveShell watches tool results for the one signal that matters at run
// time: a 127 "command not found". The offending command is taken from bash's
// own message (not from guessing the first token of the call — a compound
// command names the failing part in its error). Returns true when the learned
// set changed, i.e. the caller should persist.
func (s *System) ObserveShell(output string, exit int) bool {
	if exit != 127 {
		return false
	}
	m := notFoundRe.FindStringSubmatch(output)
	if m == nil || m[1] == "" {
		return false
	}
	if s.Learned == nil {
		s.Learned = make(map[string]string)
	}
	if s.Learned[m[1]] == "missing" {
		return false
	}
	s.Learned[m[1]] = "missing"
	return true
}

// MergeLearned folds the previous session's learned facts into this fresh
// probe — but a stale "missing" is dropped the moment the tool is on PATH
// again. Freshness always wins over memory. Tools outside the fixed probe list
// get their own probe here; otherwise a 127 learned once would stick forever,
// even after the tool was installed.
func (s *System) MergeLearned(old *System) {
	if old == nil || old.Learned == nil {
		return
	}
	if s.Learned == nil {
		s.Learned = make(map[string]string)
	}
	for tool, state := range old.Learned {
		if state != "missing" {
			continue
		}
		probed, inProbe := s.Tools[tool]
		if probed {
			continue // probe says it exists now — stale fact dropped
		}
		if !inProbe && probeTools([]string{tool})[tool] {
			continue // installed since — stale fact dropped
		}
		s.Learned[tool] = state
	}
	if len(s.Learned) == 0 {
		s.Learned = nil
	}
}

// Load reads a previous known-system.json. A missing/corrupt file is not an
// error condition worth breaking a session for — the caller decides.
func Load(path string) (*System, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s System
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the record (0600: it is a local fact file inside the operator's
// hermes-hands home, next to secrets — keep the same posture).
func (s *System) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
