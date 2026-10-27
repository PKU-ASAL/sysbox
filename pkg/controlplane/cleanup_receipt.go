package controlplane

import "time"

// CleanupReceipt is an owning-agent assertion tied to one completed destroy.
// Only lease-checked completion may persist it as metadata deletion authority.
type CleanupReceipt struct {
	Version     int       `json:"version"`
	Topology    string    `json:"topology"`
	TopologyUID string    `json:"topology_uid"`
	AgentID     string    `json:"agent_id"`
	RunID       string    `json:"run_id"`
	QueuedAt    time.Time `json:"queued_at"`
	Revision    string    `json:"revision"`
	Attempt     int       `json:"attempt"`
	LeaseOwner  string    `json:"lease_owner"`
	StateSerial int64     `json:"state_serial"`
	ObservedAt  time.Time `json:"observed_at"`
	Empty       bool      `json:"empty"`
}
