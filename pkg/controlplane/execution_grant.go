package controlplane

import "time"

// ExecutionScope uses persistent identities, never display names. Empty
// ResourceUID denotes the whole topology and conflicts with all its resources.
type ExecutionScope struct {
	TopologyUID string `json:"topology_uid"`
	ResourceUID string `json:"resource_uid,omitempty"`
	Generation  int64  `json:"generation,omitempty"`
}

type AgentSession struct {
	AgentID       string    `json:"agent_id"`
	ID            string    `json:"id"`
	IncarnationID string    `json:"incarnation_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type GrantState string

const (
	GrantActive  GrantState = "active"
	GrantRevoked GrantState = "revoked"
	GrantStopped GrantState = "stopped"
)

// ExecutionGrant is an authority record, not a bearer credential. Callers must
// authenticate the issuing control plane and the resource host independently.
type ExecutionGrant struct {
	Scope       ExecutionScope `json:"scope"`
	AgentID     string         `json:"agent_id"`
	SessionID   string         `json:"session_id"`
	ActionID    string         `json:"action_id"`
	CommandID   string         `json:"command_id"`
	Attempt     uint64         `json:"attempt"`
	Epoch       uint64         `json:"epoch"`
	Operation   string         `json:"operation"`
	Digest      string         `json:"digest"`
	ExpiresAt   time.Time      `json:"expires_at"`
	State       GrantState     `json:"state"`
	StopReceipt string         `json:"stop_receipt,omitempty"`
}
