// Package sensor defines the Sensor interface and the Event schema for
// sysbox Phase 2 observation.
//
// Design contract (method A, strict):
//   - SessionID is populated iff the event's cgroup_id is registered as a
//     session cgroup by the Labeler.
//   - IsAttack == (SessionID != "").
//   - ProcessTree is maintained internally by ProcessTreeBuilder; it is NOT
//     exported on events (Phase 3 Matcher queries it via sensor.ProcessTree()).
package sensor

import "context"

// Event is a normalized observation from a sysbox node.
//
// Raw events (events.jsonl) contain only tracee-observed fields.
// Matcher-populated fields (matched_prediction, agent_step, ttp, ioc, is_attack)
// appear only in annotated_events.jsonl produced by `sysbox match run`.
//
// Attribution model (Phase 3):
//   - Prediction Matcher: compares agent tool-call predictions against events
//   - IoC Engine: scans events for known attack tool signatures
//   - is_attack = matched_prediction OR ioc != ""
//
// session_id is retained as an optional external trace correlation field
// (e.g. Langfuse run ID), not as the primary attribution mechanism.
type Event struct {
	// Raw tracee fields — always present
	NodeID    string         `json:"node_id"`
	CgroupID  uint64         `json:"cgroup_id"`  // kernel cgroup id; metadata only in Phase 3
	Timestamp int64          `json:"ts"`         // unix nanoseconds
	PID       int            `json:"pid"`
	PPID      int            `json:"ppid"`
	Type      string         `json:"type"`       // "syscall" | "net" | "file"
	Name      string         `json:"name"`       // e.g. "execve"
	Args      map[string]any `json:"args"`

	// External trace correlation (optional)
	SessionID string `json:"session_id,omitempty"` // Langfuse run ID or similar

	// Matcher-populated — only in annotated_events.jsonl
	MatchedPrediction bool   `json:"matched_prediction,omitempty"`
	AgentStep         int    `json:"agent_step,omitempty"`
	TTP               string `json:"ttp,omitempty"`  // MITRE ATT&CK ID
	IoC               string `json:"ioc,omitempty"`  // IoC rule ID that fired
	IsAttack          bool   `json:"is_attack"`      // matched_prediction OR ioc != ""
}

// Sensor observes a running node via an eBPF backend (Tracee).
type Sensor interface {
	// Start begins observation and returns a channel of labelled events.
	// containerID is the Docker container ID used to scope Tracee.
	Start(ctx context.Context, nodeID, containerID string) (<-chan Event, error)
	// Stop gracefully shuts down the sensor subprocess.
	Stop() error
	// ProcessTree returns the internal ProcessTreeBuilder.
	// Phase 3 Matcher queries it; events do not carry process tree data.
	ProcessTree() *ProcessTreeBuilder
}
