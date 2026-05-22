package runtime

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/substrate"
)

type RecoveryContext string

const (
	RecoveryContextRefresh    RecoveryContext = "refresh"
	RecoveryContextCheckpoint RecoveryContext = "checkpoint"
)

type RecoveryDecision string

const (
	RecoveryDecisionNoop         RecoveryDecision = "noop"
	RecoveryDecisionAdopt        RecoveryDecision = "adopt"
	RecoveryDecisionRecoverState RecoveryDecision = "recover_state"
	RecoveryDecisionMarkDrift    RecoveryDecision = "mark_drift"
	RecoveryDecisionNotFound     RecoveryDecision = "not_found"
	RecoveryDecisionUnknown      RecoveryDecision = "unknown"
)

type RecoveryInput struct {
	Context              RecoveryContext
	ResourceType         string
	Provider             string
	HasState             bool
	HasCheckpoint        bool
	StateRecorded        bool
	RecoverableArtifacts bool
	Observation          substrate.NodeObservation
}

type RecoveryPlan struct {
	Decision RecoveryDecision
	Reason   string
}

func DecideNodeRecovery(in RecoveryInput) RecoveryPlan {
	switch in.Context {
	case RecoveryContextRefresh:
		return decideRefreshRecovery(in)
	case RecoveryContextCheckpoint:
		return decideCheckpointRecovery(in)
	default:
		return RecoveryPlan{
			Decision: RecoveryDecisionUnknown,
			Reason:   "unknown recovery context",
		}
	}
}

func decideRefreshRecovery(in RecoveryInput) RecoveryPlan {
	obs := in.Observation
	if !in.HasState {
		return RecoveryPlan{Decision: RecoveryDecisionMarkDrift, Reason: "resource missing from state"}
	}
	if obs.Status == substrate.NodeStatusUnknown {
		return RecoveryPlan{Decision: RecoveryDecisionUnknown, Reason: observationReason(obs, "node status unknown")}
	}
	if obs.Running && obs.Healthy {
		return RecoveryPlan{Decision: RecoveryDecisionNoop, Reason: observationReason(obs, "node running")}
	}
	if !obs.Exists || obs.Status == substrate.NodeStatusMissing {
		return RecoveryPlan{Decision: RecoveryDecisionMarkDrift, Reason: observationReason(obs, "node missing")}
	}
	if obs.Running && !obs.Healthy {
		return RecoveryPlan{Decision: RecoveryDecisionMarkDrift, Reason: observationReason(obs, "node unhealthy")}
	}
	return RecoveryPlan{Decision: RecoveryDecisionMarkDrift, Reason: observationReason(obs, fmt.Sprintf("node %s", obs.Status))}
}

func decideCheckpointRecovery(in RecoveryInput) RecoveryPlan {
	obs := in.Observation
	if in.HasState || in.StateRecorded {
		return RecoveryPlan{Decision: RecoveryDecisionNoop, Reason: "resource already recorded in state"}
	}
	if !in.HasCheckpoint {
		return RecoveryPlan{Decision: RecoveryDecisionNotFound, Reason: "checkpoint missing"}
	}
	if !in.RecoverableArtifacts {
		return RecoveryPlan{Decision: RecoveryDecisionNotFound, Reason: "recoverable artifacts missing"}
	}
	if obs.Status == substrate.NodeStatusUnknown {
		return RecoveryPlan{Decision: RecoveryDecisionUnknown, Reason: observationReason(obs, "node status unknown")}
	}
	if obs.Running && obs.Healthy {
		return RecoveryPlan{Decision: RecoveryDecisionAdopt, Reason: observationReason(obs, "node still running")}
	}
	if obs.Exists || !obs.Running {
		return RecoveryPlan{Decision: RecoveryDecisionRecoverState, Reason: observationReason(obs, "node not running but artifacts are recoverable")}
	}
	return RecoveryPlan{Decision: RecoveryDecisionNotFound, Reason: observationReason(obs, "node not recoverable")}
}

func observationReason(obs substrate.NodeObservation, fallback string) string {
	if obs.Reason != "" {
		return obs.Reason
	}
	if obs.Status != "" {
		return string(obs.Status)
	}
	return fallback
}
