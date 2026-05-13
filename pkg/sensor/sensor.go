// Package sensor defines the Event schema and tracee JSON parsing helpers.
package sensor

import (
	"encoding/json"
)

// RawFields parses Event.Raw into a flat map for field-level access in
// post-processing layers (IoC engine, matchers, etc.).
// Returns nil if Raw is empty or invalid JSON.
func (e Event) RawFields() map[string]any {
	if len(e.Raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(e.Raw, &m); err != nil {
		return nil
	}
	return m
}

// Event is a normalized observation from a sysbox node.
//
// The schema is intentionally shallow: NodeID/Timestamp/Category provide
// just enough structure for routing and storage, while Raw preserves the
// full platform-specific event for post-processing (PID tree reconstruction,
// causal graph, etc.).
//
// PID and PPID are extracted as a convenience for the current Linux/tracee
// backend; they remain available for backward compatibility with the matcher.
type Event struct {
	NodeID    string          `json:"node_id"`
	Timestamp int64           `json:"ts"`            // unix nanoseconds
	Category  string          `json:"category"`      // "exec" | "file" | "net" | "process"
	PID       int             `json:"pid,omitempty"`
	PPID      int             `json:"ppid,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"` // full platform event JSON
}


