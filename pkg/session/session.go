// Package session manages cgroup v2-based session anchoring for sysbox nodes.
//
// A session corresponds to one attacker entry: an SSH connection, or any other
// privileged entry that the sysbox-sshd-hook intercepts.  The hook creates a
// dedicated cgroup for each session; the kernel ensures that all descendant
// processes inherit that cgroup.  The Labeler maps cgroup_id → session_id so
// the sensor can annotate Tracee events.
package session

import "time"

// Session describes one anchored entry into a node.
type Session struct {
	ID        string     `json:"id"`
	NodeID    string     `json:"node_id"`
	User      string     `json:"user"`
	SourceIP  string     `json:"source_ip,omitempty"`
	CgroupID  uint64     `json:"cgroup_id"`
	StartTime time.Time  `json:"start_time"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}
