package controlplane

import "time"

const RunRecoveryCapability = "run-recovery.v1"
const RunRecoveryCommand = "run_recovery"
const RunRecoveryObserve = "observe"
const RunRecoveryRecover = "recover"

// RunRecoveryReport is evidence, not a Run completion or a replacement state.
type RunRecoveryReport struct {
	RunAttempt    int                 `json:"run_attempt"`
	RunLeaseOwner string              `json:"run_lease_owner,omitempty"`
	CommandID     string              `json:"command_id"`
	RunID         string              `json:"run_id"`
	AgentID       string              `json:"agent_id"`
	QueuedAt      time.Time           `json:"queued_at"`
	Revision      string              `json:"revision"`
	Mode          string              `json:"mode"`
	TopologyUID   string              `json:"topology_uid"`
	StateSerial   int64               `json:"state_serial"`
	ObservedAt    time.Time           `json:"observed_at"`
	Resources     []RecoveredResource `json:"resources"`
}
type RecoveredResource struct {
	ActionID      string `json:"action_id"`
	Resource      string `json:"resource"`
	OwnerUID      string `json:"owner_uid"`
	ResourceUID   string `json:"resource_uid"`
	Generation    int64  `json:"generation"`
	ExternalID    string `json:"external_id,omitempty"`
	Status        string `json:"status"`
	JournalStatus string `json:"journal_status"`
}
type RunRecoveryAuthorization struct {
	Run      Run             `json:"run"`
	Mode     string          `json:"mode"`
	Revision *GlobalRevision `json:"revision,omitempty"`
}
