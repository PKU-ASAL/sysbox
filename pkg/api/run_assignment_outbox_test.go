package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRunAssignmentAndCommandCommitTogether(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		queued := controlplane.Run{ID: "assignment", Topology: "lab", Operation: "apply", Op: "apply", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC()}
		require.NoError(t, s.SaveRun(ctx, queued))
		assigned := queued
		assigned.MarkAssigned("agent", time.Now().UTC())
		cmd := controlplane.AgentCommand{TopologyUID: "owner", ID: "run-assignment", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, Run: &assigned}
		_, _, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: assigned, Command: cmd, Fingerprint: "run-dispatch:assignment:apply:agent"})
		require.NoError(t, err)
		stored, err := s.GetRun(ctx, queued.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunAssigned, stored.Status)
		require.Equal(t, "agent", stored.AgentID)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
	})
}

func TestRunAssignmentRejectsChangedExecutionBinding(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		queued := controlplane.Run{ID: "assignment", Topology: "lab", Operation: "apply", Op: "apply", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC(), Revision: "original"}
		require.NoError(t, s.SaveRun(ctx, queued))
		assigned := queued
		assigned.MarkAssigned("agent", time.Now().UTC())
		assigned.Revision = "different"
		cmd := controlplane.AgentCommand{TopologyUID: "owner", ID: "run-assignment", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, Run: &assigned}
		_, _, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: assigned, Command: cmd, Fingerprint: "run-dispatch:assignment:apply:agent"})
		require.Error(t, err)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		stored, err := s.GetRun(ctx, queued.ID)
		require.NoError(t, err)
		require.Equal(t, "original", stored.Revision)
		require.Equal(t, controlplane.RunQueued, stored.Status)
	})
}

func TestRunAssignmentCommandConflictPreservesQueuedState(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		queued := controlplane.Run{ID: "conflict", Topology: "lab", Operation: "apply", Op: "apply", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC()}
		require.NoError(t, s.SaveRun(ctx, queued))
		assigned := queued
		assigned.MarkAssigned("agent", time.Now().UTC())
		cmd := controlplane.AgentCommand{TopologyUID: "owner", ID: "run-conflict", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, Run: &assigned}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		_, _, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: assigned, Command: cmd, Fingerprint: "run-dispatch:conflict"})
		require.Error(t, err)
		stored, err := s.GetRun(ctx, queued.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunQueued, stored.Status)
		require.Empty(t, stored.AgentID)
	})
}
