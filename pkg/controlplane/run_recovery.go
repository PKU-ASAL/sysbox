package controlplane

import (
	"time"

	"github.com/oslab/sysbox/pkg/model"
)

const RunRecoveryCapability = "run-recovery.v1"
const RunRecoveryInputsCapability = "run-recovery-inputs.v1"
const RunRecoveryCommand = "run_recovery"
const RunRecoveryObserve = "observe"
const RunRecoveryRecover = "recover"

// RunRecoveryReport is evidence, not a Run completion or a replacement state.
type RunRecoveryReport struct {
	HostObservation *HostObservationProof     `json:"host_observation,omitempty"`
	RunAttempt      int                       `json:"run_attempt"`
	RunLeaseOwner   string                    `json:"run_lease_owner,omitempty"`
	CommandID       string                    `json:"command_id"`
	RunID           string                    `json:"run_id"`
	AgentID         string                    `json:"agent_id"`
	QueuedAt        time.Time                 `json:"queued_at"`
	Revision        string                    `json:"revision"`
	Mode            string                    `json:"mode"`
	TopologyUID     string                    `json:"topology_uid"`
	StateSerial     int64                     `json:"state_serial"`
	ObservedAt      time.Time                 `json:"observed_at"`
	Resources       []model.RecoveredResource `json:"resources"`
}

type RunRecoveryAuthorization struct {
	// Inputs are transient, fingerprint-validated values; never part of command persistence.
	Inputs           map[string]string        `json:"inputs"`
	FenceObservation *FenceObservationBinding `json:"fence_observation,omitempty"`
	LeaseUntil       time.Time                `json:"lease_until"`
	Run              Run                      `json:"run"`
	Mode             string                   `json:"mode"`
	Revision         *GlobalRevision          `json:"revision,omitempty"`
}
