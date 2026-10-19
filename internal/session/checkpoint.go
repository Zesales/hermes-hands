package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ChatCheckpoint is the conversation Hermes reads from a compaction on: the
// transcript hermes-hands rendered when it compacted, frozen in
// <id>/chat-checkpoints/NNNN.json. The event log stays the whole chat; the
// compaction's event names its checkpoint, so a viewer (the terminal, an
// editor plugin) can put the old chat and the compacted one side by side.
// What Hermes reads is the latest checkpoint plus every event after Seq,
// verbatim — a frozen prefix: a later version's rendering never changes it,
// and prompt caches keep it.
type ChatCheckpoint struct {
	V         int    `json:"v"`
	N         int    `json:"n"`    // the session's compaction number
	At        string `json:"at"`   // RFC 3339
	Seq       int    `json:"seq"`  // the last event it covers
	Turn      int    `json:"turn"` // the turn it was taken in
	Depth     string `json:"depth"`
	Before    int    `json:"before"` // estimated tokens before and after
	After     int    `json:"after"`
	Elided    string `json:"elided,omitempty"`    // "#1–#212": the outputs it carries as lines
	Collapsed string `json:"collapsed,omitempty"` // "turns 1–5": sent as question + answer
	Messages  []Msg  `json:"messages"`
}

// ChatCheckpointVersion is ChatCheckpoint.V; bump it only for an
// incompatible change.
const ChatCheckpointVersion = 1

const checkpointDir = "chat-checkpoints"

// SaveChatCheckpoint writes cp as checkpoint cp.N of rec and returns its
// path relative to the session directory — what the KindCompact event
// names.
func (s *Store) SaveChatCheckpoint(rec *Record, cp *ChatCheckpoint) (string, error) {
	if err := validID(rec.ID); err != nil {
		return "", err
	}
	cp.V = ChatCheckpointVersion
	if cp.At == "" {
		cp.At = s.nowFn().UTC().Format(time.RFC3339)
	}
	b, err := json.MarshalIndent(cp, "", " ")
	if err != nil {
		return "", err
	}
	rel := fmt.Sprintf("%s/%04d.json", checkpointDir, cp.N)
	if err := os.MkdirAll(s.sessionDir(rec.ID)+"/"+checkpointDir, 0o700); err != nil {
		return "", err
	}
	return rel, os.WriteFile(s.sessionDir(rec.ID)+"/"+rel, append(b, '\n'), 0o600)
}

// LoadChatCheckpoint reads the checkpoint at rel (as a KindCompact event
// names it).
func (s *Store) LoadChatCheckpoint(rec *Record, rel string) (ChatCheckpoint, error) {
	var cp ChatCheckpoint
	if err := validID(rec.ID); err != nil {
		return cp, err
	}
	if filepath.Dir(rel) != checkpointDir || strings.Contains(rel, "..") {
		return cp, fmt.Errorf("not a chat checkpoint: %q", rel)
	}
	b, err := os.ReadFile(s.sessionDir(rec.ID) + "/" + rel)
	if err != nil {
		return cp, err
	}
	if err := json.Unmarshal(b, &cp); err != nil {
		return cp, fmt.Errorf("chat checkpoint %s: %v", rel, err)
	}
	return cp, nil
}

// Join appends b to a; the message at the seam is merged when both sides
// have the same role, so the result still alternates.
func Join(a, b []Msg) []Msg {
	out := append([]Msg(nil), a...)
	for _, m := range b {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content += "\n\n" + m.Content
			continue
		}
		out = append(out, m)
	}
	return out
}

// After is the events of evs after seq — what Hermes reads verbatim behind
// a checkpoint.
func After(evs []Event, seq int) []Event {
	for i, e := range evs {
		if e.Seq > seq {
			return evs[i:]
		}
	}
	return nil
}
