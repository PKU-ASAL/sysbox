// Plan and health types are the wire-level contract between the runtime
// engine, the API control plane, and the web UI. They live here (not in
// pkg/runtime) so DTO consumers never depend on the engine;
// pkg/runtime references them directly in its plan and health code.
package controlplane

import (
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
)

type PlanActionType string

const (
	PlanActionNoop    PlanActionType = "no-op"
	PlanActionCreate  PlanActionType = "create"
	PlanActionUpdate  PlanActionType = "update"
	PlanActionReplace PlanActionType = "replace"
	PlanActionDelete  PlanActionType = "delete"
	PlanActionRead    PlanActionType = "read"
	PlanActionSkip    PlanActionType = "skip"
)

type FieldChange struct {
	Before          any  `json:"before,omitempty"`
	After           any  `json:"after,omitempty"`
	RequiresReplace bool `json:"requires_replace,omitempty"`
	Sensitive       bool `json:"sensitive,omitempty"`
	Computed        bool `json:"computed,omitempty"`
}

type PlanAction struct {
	Resource string                 `json:"resource"`
	Type     string                 `json:"type"`
	Name     string                 `json:"name"`
	Action   PlanActionType         `json:"action"`
	Reason   string                 `json:"reason,omitempty"`
	Changes  map[string]FieldChange `json:"changes,omitempty"`
}

func (a PlanAction) Address() address.Address {
	return address.Resource(a.Type, a.Name)
}

type ResourceHealthStatus string

const (
	ResourceHealthHealthy ResourceHealthStatus = "healthy"
	ResourceHealthDrifted ResourceHealthStatus = "drifted"
	ResourceHealthUnknown ResourceHealthStatus = "unknown"
)

// RecoveryDecision is the outcome of a recovery-policy evaluation for a
// resource (see recovery policy in pkg/runtime).
type RecoveryDecision string

const (
	RecoveryDecisionNoop         RecoveryDecision = "noop"
	RecoveryDecisionAdopt        RecoveryDecision = "adopt"
	RecoveryDecisionRecoverState RecoveryDecision = "recover_state"
	RecoveryDecisionMarkDrift    RecoveryDecision = "mark_drift"
	RecoveryDecisionNotFound     RecoveryDecision = "not_found"
	RecoveryDecisionUnknown      RecoveryDecision = "unknown"
)

type TopologyHealth struct {
	Status    ResourceHealthStatus `json:"status"`
	Healthy   int                  `json:"healthy"`
	Drifted   int                  `json:"drifted"`
	Unknown   int                  `json:"unknown"`
	Resources []ResourceHealth     `json:"resources"`
}

type ResourceHealth struct {
	Resource    string                         `json:"resource"`
	Type        string                         `json:"type"`
	Name        string                         `json:"name"`
	Provider    string                         `json:"provider,omitempty"`
	Status      ResourceHealthStatus           `json:"status"`
	Reason      string                         `json:"reason,omitempty"`
	Decision    RecoveryDecision               `json:"decision,omitempty"`
	Observation *substrate.NodeObservation     `json:"observation,omitempty"`
	Checks      map[string]ResourceCheckHealth `json:"checks,omitempty"`
}

type ResourceCheckHealth struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}
