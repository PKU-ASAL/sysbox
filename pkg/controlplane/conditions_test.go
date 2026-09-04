package controlplane

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func run(status RunStatus, op string) *Run {
	r := &Run{Status: status, Op: op}
	if op == "" {
		r.Op = "apply"
	}
	return r
}

func healthyHealth() *TopologyHealth {
	return &TopologyHealth{Status: ResourceHealthHealthy}
}

func statusOf(t *testing.T, s TopologyStatus, typ string) Condition {
	t.Helper()
	for _, c := range s.Conditions {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("condition %q not found", typ)
	return Condition{}
}

// A topology whose apply completed, whose resources are healthy, and whose
// assertions all passed is ready.
func TestComputeTopologyStatusReady(t *testing.T) {
	s := ComputeTopologyStatus(run(RunDone, "apply"), healthyHealth(), &AssertionResult{})

	require.Equal(t, PhaseReady, s.Phase)
	require.Equal(t, ConditionTrue, statusOf(t, s, ConditionApplied).Status)
	require.Equal(t, ConditionTrue, statusOf(t, s, ConditionProvisioned).Status)
	require.Equal(t, ConditionTrue, statusOf(t, s, ConditionReady).Status)
}

// A failed apply makes Ready false, and the reason names the failing condition.
func TestComputeTopologyStatusFailedApply(t *testing.T) {
	s := ComputeTopologyStatus(run(RunFailed, "apply"), healthyHealth(), &AssertionResult{})

	require.Equal(t, PhaseFailed, s.Phase)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionApplied).Status)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionReady).Status)
	require.Equal(t, "AppliedFailed", statusOf(t, s, ConditionReady).Reason)
}

// Drifted resources make Ready false via Provisioned.
func TestComputeTopologyStatusDrifted(t *testing.T) {
	s := ComputeTopologyStatus(run(RunDone, "apply"), &TopologyHealth{Status: ResourceHealthDrifted}, &AssertionResult{})

	require.Equal(t, PhaseFailed, s.Phase)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionProvisioned).Status)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionReady).Status)
	require.Equal(t, "ProvisionedFailed", statusOf(t, s, ConditionReady).Reason)
}

// A failed assertion makes Ready false, carrying the author's message and the
// failing check names — this is what a consumer gates on.
func TestComputeTopologyStatusFailedAssertion(t *testing.T) {
	s := ComputeTopologyStatus(run(RunDone, "apply"), healthyHealth(), &AssertionResult{
		Message:      "edge 不应能连到 core:5432，但连上了",
		FailedChecks: []string{"edge_cannot_reach_core"},
	})

	require.Equal(t, PhaseFailed, s.Phase)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionAsserted).Status)
	require.Equal(t, "CheckFailed", statusOf(t, s, ConditionAsserted).Reason)
	require.Equal(t, "edge 不应能连到 core:5432，但连上了", statusOf(t, s, ConditionAsserted).Message)
	require.Equal(t, []string{"edge_cannot_reach_core"}, statusOf(t, s, ConditionAsserted).FailedChecks)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionReady).Status)
	require.Equal(t, "AssertedFailed", statusOf(t, s, ConditionReady).Reason)
}

// The critical distinction: an assertion that has not been evaluated must not
// make Ready false. A topology that applied and provisioned cleanly is "not yet
// known ready", not "failed" — every apply passes through this window, so
// misreading Unknown as False would mark every deployment as broken.
func TestComputeTopologyStatusUnevaluatedAssertionIsUnknownNotFalse(t *testing.T) {
	s := ComputeTopologyStatus(run(RunDone, "apply"), healthyHealth(), nil)

	require.Equal(t, ConditionUnknown, statusOf(t, s, ConditionAsserted).Status)
	require.Equal(t, ConditionUnknown, statusOf(t, s, ConditionReady).Status,
		"an unevaluated assertion must yield Unknown Ready, not False")
	require.Equal(t, PhaseConverging, s.Phase)
}

// A run still in flight means the apply has no verdict yet: Applied is Unknown,
// not False.
func TestComputeTopologyStatusRunningIsUnknownNotFalse(t *testing.T) {
	s := ComputeTopologyStatus(run(RunRunning, "apply"), healthyHealth(), &AssertionResult{})

	require.Equal(t, ConditionUnknown, statusOf(t, s, ConditionApplied).Status)
	require.Equal(t, PhaseConverging, s.Phase)
}

// An active destroy run puts the topology in the destroying phase.
func TestComputeTopologyStatusDestroying(t *testing.T) {
	s := ComputeTopologyStatus(run(RunRunning, "destroy"), healthyHealth(), nil)

	require.Equal(t, PhaseDestroying, s.Phase)
}

// A topology with no run and no observation is entirely unknown: nothing has
// been evaluated, nothing has failed.
func TestComputeTopologyStatusNeverObserved(t *testing.T) {
	s := ComputeTopologyStatus(nil, nil, nil)

	for _, c := range s.Conditions {
		require.Equal(t, ConditionUnknown, c.Status, "condition %q should be unknown", c.Type)
	}
	require.Equal(t, PhaseConverging, s.Phase)
}

// False dominates Unknown in the conjunction: a failed apply is failed even if
// the assertion was never evaluated.
func TestComputeTopologyStatusFalseDominatesUnknown(t *testing.T) {
	s := ComputeTopologyStatus(run(RunFailed, "apply"), nil, nil)

	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionApplied).Status)
	require.Equal(t, ConditionUnknown, statusOf(t, s, ConditionProvisioned).Status)
	require.Equal(t, ConditionFalse, statusOf(t, s, ConditionReady).Status,
		"a False input must dominate an Unknown one")
}

// The observed revision is carried through from the run.
func TestComputeTopologyStatusCarriesRevision(t *testing.T) {
	r := run(RunDone, "apply")
	r.Revision = "sha256:abc"
	s := ComputeTopologyStatus(r, healthyHealth(), &AssertionResult{})

	require.Equal(t, "sha256:abc", s.ObservedRevision)
}

// The apply's terminal time is the observed time of the Applied condition.
func TestComputeTopologyStatusRecordsAppliedTime(t *testing.T) {
	r := run(RunDone, "apply")
	r.EndedAt = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	s := ComputeTopologyStatus(r, healthyHealth(), &AssertionResult{})

	require.Equal(t, r.EndedAt, statusOf(t, s, ConditionApplied).ObservedAt)
}
