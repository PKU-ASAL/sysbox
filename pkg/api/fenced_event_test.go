package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestFencedCommandRejectsMissingProofWithoutPublishingEvent(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "fenced", AgentID: "agent", Type: "node_operation", ActionID: "action", ResourceUID: "resource", Generation: 2, LeaseEpoch: 3}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		registry := newAgentRegistry()
		svc := &AgentService{store: s, registry: registry}
		svc.RecordCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: cmd.ID, AgentID: cmd.AgentID, Status: "completed"})
		got, err := svc.FindCommand(ctx, cmd.AgentID, cmd.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.AgentCommandStatusQueued, got.Status)
		require.Empty(t, registry.ListCommandEvents("agent"))
	})
}

func TestFencedTerminalEventCannotRetireNewLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "renewed", AgentID: "agent", Type: "node_operation", Status: "leased", LeaseOwner: "old", Attempt: 1, LeaseUntil: time.Now().Add(-time.Hour), ActionID: "action", ResourceUID: "resource", Generation: 2, LeaseEpoch: 3}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		fresh, ok, err := s.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "new", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		event := controlplane.AgentCommandEvent{CommandID: cmd.ID, AgentID: "agent", Type: cmd.Type, Status: "completed", LeaseOwner: "old", Attempt: 1, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, LeaseEpoch: cmd.LeaseEpoch}
		_, err = s.ApplyAgentCommandEvent(ctx, event)
		require.ErrorIs(t, err, persistence.ErrConflict)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, fresh.Attempt, commands[0].Attempt)
		event.LeaseOwner = fresh.LeaseOwner
		event.Attempt = fresh.Attempt
		completed, err := s.ApplyAgentCommandEvent(ctx, event)
		require.NoError(t, err)
		require.Equal(t, "completed", completed.Status)
		commands, err = s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}
