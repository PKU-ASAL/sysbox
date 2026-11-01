package runtime

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/substrate"
)

type RecoveryContext string

const (
	RecoveryContextRefresh RecoveryContext = "refresh"
)

type RecoveryInput struct {
	Context      RecoveryContext
	ResourceType string
	Provider     string
	HasState     bool
	Observation  substrate.NodeObservation
}

type RecoveryPlan struct {
	Decision model.RecoveryDecision
	Reason   string
}

func DecideNodeRecovery(in RecoveryInput) RecoveryPlan {
	switch in.Context {
	case RecoveryContextRefresh:
		return decideRefreshRecovery(in)
	default:
		return RecoveryPlan{
			Decision: model.RecoveryDecisionUnknown,
			Reason:   "unknown recovery context",
		}
	}
}

func decideRefreshRecovery(in RecoveryInput) RecoveryPlan {
	obs := in.Observation
	if !in.HasState {
		return RecoveryPlan{Decision: model.RecoveryDecisionMarkDrift, Reason: "resource missing from state"}
	}
	if obs.Status == substrate.NodeStatusUnknown {
		return RecoveryPlan{Decision: model.RecoveryDecisionUnknown, Reason: observationReason(obs, "node status unknown")}
	}
	if obs.Running && obs.Healthy {
		return RecoveryPlan{Decision: model.RecoveryDecisionNoop, Reason: observationReason(obs, "node running")}
	}
	if !obs.Exists || obs.Status == substrate.NodeStatusMissing {
		return RecoveryPlan{Decision: model.RecoveryDecisionMarkDrift, Reason: observationReason(obs, "node missing")}
	}
	if obs.Running && !obs.Healthy {
		return RecoveryPlan{Decision: model.RecoveryDecisionMarkDrift, Reason: observationReason(obs, "node unhealthy")}
	}
	return RecoveryPlan{Decision: model.RecoveryDecisionMarkDrift, Reason: observationReason(obs, fmt.Sprintf("node %s", obs.Status))}
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
