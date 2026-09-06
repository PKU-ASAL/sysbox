package controlplane

import "time"

// ConditionStatus is the Kubernetes-style tri-state of a readiness condition.
//
// The distinction between Unknown and False is load-bearing: Unknown means "not
// evaluated yet", False means "evaluated and failed". Every apply passes through
// a window where checks have not run; collapsing that window into False would
// read "not done yet" as "definitively broken" on every single deployment.
type ConditionStatus string

const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// Condition is one fact about a topology's readiness. It is a projection of
// facts that already exist elsewhere (a run's terminal state, topology health,
// assertion results) — never a new source of writable state.
type Condition struct {
	Type         string          `json:"type"`
	Status       ConditionStatus `json:"status"`
	ObservedAt   time.Time       `json:"observed_at,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Message      string          `json:"message,omitempty"`
	FailedChecks []string        `json:"failed_checks,omitempty"`
}

// Well-known condition types, in dependency order.
const (
	ConditionApplied     = "Applied"
	ConditionProvisioned = "Provisioned"
	ConditionAsserted    = "Asserted"
	ConditionReady       = "Ready"
)

// TopologyPhase is the coarse life-cycle phase a topology is in.
type TopologyPhase string

const (
	PhaseConverging TopologyPhase = "converging"
	PhaseReady      TopologyPhase = "ready"
	PhaseFailed     TopologyPhase = "failed"
	PhaseDestroying TopologyPhase = "destroying"
	PhaseGone       TopologyPhase = "gone"
)

// TopologyStatus is the readiness projection reported on a topology.
type TopologyStatus struct {
	ObservedRevision string         `json:"observed_revision,omitempty"`
	Phase            TopologyPhase  `json:"phase"`
	DeadlineAt       *time.Time     `json:"deadline_at,omitempty"`
	Conditions       []Condition    `json:"conditions"`
	Nodes            []TopologyNode `json:"nodes,omitempty"`
	Outputs          map[string]any `json:"outputs,omitempty"`
}

// TopologyNode is a read-only projection of a sysbox_node resource in state.
// It answers "what nodes exist, where do they live, and are they running"
// without exposing the full resource payload.
type TopologyNode struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	State   string `json:"state,omitempty"`
}

// AssertionResult is the outcome of check/assert evaluation. It carries the
// author-written failure message and the names of the checks that failed.
type AssertionResult struct {
	Message      string   `json:"message,omitempty"`
	FailedChecks []string `json:"failed_checks,omitempty"`
}

// ComputeTopologyStatus projects the readiness conditions of a topology from
// its latest run, current health and assertion outcome.
//
// latestRun is the most recent run for the topology (nil if none); health is
// the current topology health (nil if not yet observed). assertion is nil until
// check/assert evaluation has produced a verdict — an unevaluated assertion is
// Unknown, not False, so it neither blocks nor certifies readiness.
func ComputeTopologyStatus(latestRun *Run, health *TopologyHealth, assertion *AssertionResult) TopologyStatus {
	applied := conditionApplied(latestRun)
	provisioned := conditionProvisioned(health)
	asserted := conditionAsserted(assertion)
	ready := combineReady(applied, provisioned, asserted)

	return TopologyStatus{
		ObservedRevision: observedRevision(latestRun),
		Phase:            derivePhase(latestRun, ready),
		Conditions:       []Condition{applied, provisioned, asserted, ready},
	}
}

func conditionApplied(run *Run) Condition {
	c := Condition{Type: ConditionApplied, Status: ConditionUnknown}
	if run == nil {
		return c
	}
	if !run.EndedAt.IsZero() {
		c.ObservedAt = run.EndedAt
	}
	switch run.Status {
	case RunDone:
		c.Status = ConditionTrue
	case RunFailed, RunCancelled:
		c.Status = ConditionFalse
		c.Reason = "ApplyFailed"
		if run.Err != "" {
			c.Message = run.Err
		}
	default:
		// queued / assigned / running: the apply has not reached a verdict.
		c.Status = ConditionUnknown
	}
	return c
}

func conditionProvisioned(health *TopologyHealth) Condition {
	c := Condition{Type: ConditionProvisioned, Status: ConditionUnknown}
	if health == nil {
		return c
	}
	switch health.Status {
	case ResourceHealthHealthy:
		c.Status = ConditionTrue
	case ResourceHealthDrifted:
		c.Status = ConditionFalse
		c.Reason = "ResourceDrift"
	case ResourceHealthUnknown:
		c.Status = ConditionUnknown
	}
	return c
}

// conditionAsserted projects an assertion outcome. A nil outcome means the
// assertion has not been evaluated (Unknown); an evaluated assertion is True
// when no checks failed and False otherwise, carrying the author's message and
// the failing check names.
func conditionAsserted(assertion *AssertionResult) Condition {
	if assertion == nil {
		return Condition{Type: ConditionAsserted, Status: ConditionUnknown}
	}
	if len(assertion.FailedChecks) == 0 {
		return Condition{Type: ConditionAsserted, Status: ConditionTrue}
	}
	return Condition{
		Type:         ConditionAsserted,
		Status:       ConditionFalse,
		Reason:       "CheckFailed",
		Message:      assertion.Message,
		FailedChecks: assertion.FailedChecks,
	}
}

// combineReady computes Ready as the conjunction of its inputs: any False makes
// Ready False; otherwise any Unknown makes it Unknown; only all-True yields
// True.
func combineReady(conditions ...Condition) Condition {
	ready := Condition{Type: ConditionReady, Status: ConditionTrue}
	for _, c := range conditions {
		switch c.Status {
		case ConditionFalse:
			ready.Status = ConditionFalse
			ready.Reason = c.Type + "Failed"
			return ready
		case ConditionUnknown:
			ready.Status = ConditionUnknown
		}
	}
	return ready
}

func derivePhase(run *Run, ready Condition) TopologyPhase {
	if run != nil && isActiveRun(run) {
		if run.Op == "destroy" {
			return PhaseDestroying
		}
		return PhaseConverging
	}
	switch ready.Status {
	case ConditionTrue:
		return PhaseReady
	case ConditionFalse:
		return PhaseFailed
	default:
		return PhaseConverging
	}
}

func isActiveRun(run *Run) bool {
	switch run.Status {
	case RunQueued, RunAssigned, RunRunning:
		return true
	default:
		return false
	}
}

func observedRevision(run *Run) string {
	if run == nil {
		return ""
	}
	return run.Revision
}
