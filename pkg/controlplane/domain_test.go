package controlplane

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunLifecycleHelpers(t *testing.T) {
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	run := Run{ID: "run-1", Topology: "lab", Status: RunQueued, QueuedAt: now}

	require.True(t, run.Status.IsActive())
	require.False(t, run.Status.IsTerminal())

	run.MarkAssigned("host-a", now.Add(time.Minute))
	require.Equal(t, RunAssigned, run.Status)
	require.True(t, run.CanBeClaimedBy("host-a", now.Add(2*time.Minute)))
	require.False(t, run.CanBeClaimedBy("host-b", now.Add(2*time.Minute)))

	run.MarkRunning("lease-1", 30*time.Second, now.Add(2*time.Minute))
	require.Equal(t, RunRunning, run.Status)
	require.Equal(t, 1, run.Attempt)
	require.True(t, run.CanRenewLease("host-a", "lease-1"))
	require.False(t, run.CanRenewLease("host-a", "other"))

	run.MarkFinished(nil, now.Add(3*time.Minute))
	require.Equal(t, RunDone, run.Status)
	require.True(t, run.Status.IsTerminal())
	require.False(t, run.Recoverable)

	run.MarkFinished(errors.New("boom"), now.Add(4*time.Minute))
	require.Equal(t, RunFailed, run.Status)
	require.True(t, run.Recoverable)
	require.Equal(t, "boom", run.Err)
}

func TestAgentSchedulingHelpers(t *testing.T) {
	agent := Agent{ID: "host-a", Status: AgentStatusOnline}
	require.True(t, agent.IsSchedulable())
	require.False(t, agent.IsBlocked())

	agent.Disabled = true
	agent.Status = AgentStatusForPolicy(agent.Disabled, agent.Quarantined)
	require.Equal(t, AgentStatusDisabled, agent.Status)
	require.False(t, agent.IsSchedulable())
	require.True(t, agent.IsBlocked())

	offline := Agent{ID: "host-b", Status: AgentStatusOffline}
	require.False(t, offline.IsSchedulable())
	require.False(t, offline.IsBlocked())
}

func TestPlanCanApply(t *testing.T) {
	plan := Plan{
		ID:          "plan-1",
		Revision:    "rev-a",
		StateSerial: 7,
		Status:      PlanStatusPlanned,
		Actions:     []PlanAction{{Resource: "sysbox_node.web", Action: PlanActionCreate}},
	}

	require.NoError(t, plan.CanApply("rev-a", 7))
	require.ErrorContains(t, plan.CanApply("rev-b", 7), "stale")
	require.ErrorContains(t, plan.CanApply("rev-a", 8), "state serial")

	plan.Status = "applied"
	require.ErrorContains(t, plan.CanApply("rev-a", 7), "status is applied")

	plan.Status = PlanStatusPlanned
	plan.Actions = nil
	require.ErrorContains(t, plan.CanApply("rev-a", 7), "has no actions")
}

func TestAgentCommandLeaseHelpers(t *testing.T) {
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	cmd := AgentCommand{ID: "cmd-1", Status: AgentCommandStatusQueued}

	require.True(t, cmd.IsPending())
	require.True(t, cmd.Leasable(now))

	cmd.MarkLeased("host-a:1", time.Minute, now)
	require.Equal(t, AgentCommandStatusLeased, cmd.Status)
	require.False(t, cmd.IsPending())
	require.False(t, cmd.Leasable(now.Add(2*time.Minute)))

	cmd.MarkDelivered(now.Add(2 * time.Minute))
	require.Equal(t, AgentCommandStatusDelivered, cmd.Status)
	require.True(t, cmd.IsPending())
	require.False(t, cmd.LeaseUntil.IsZero())

	cmd.Status = AgentCommandStatusCompleted
	require.True(t, cmd.IsTerminal())
	require.False(t, cmd.Leasable(now.Add(3*time.Minute)))
}
