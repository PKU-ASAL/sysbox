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

// Event is a minimal, schema-stable observation from inside a node.
//
// Fields NOT present by design: ProcessTree, EntryPoint, any heuristic
// classification. Those are Phase 3 Matcher inputs.
type Event struct {
	NodeID    string         `json:"node_id"`
	SessionID string         `json:"session_id,omitempty"` // non-empty iff cgroup_id is a session cgroup
	CgroupID  uint64         `json:"cgroup_id"`            // raw kernel value; scrubbed before dataset export
	Timestamp int64          `json:"ts"`                   // unix nanoseconds
	PID       int            `json:"pid"`
	PPID      int            `json:"ppid"`
	Type      string         `json:"type"`  // "syscall" | "net" | "file"
	Name      string         `json:"name"`  // e.g. "execve"
	Args      map[string]any `json:"args"`
	IsAttack  bool           `json:"is_attack"` // true iff SessionID != ""
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
