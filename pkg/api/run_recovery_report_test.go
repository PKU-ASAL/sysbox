package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
)

func TestRecoveryReportPersistsWithTerminalCommand(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", QueuedAt: time.Now().UTC(), Status: controlplane.RunRunning}
		cmd := controlplane.AgentCommand{ID: "observe", AgentID: "agent", Type: controlplane.RunRecoveryCommand, RecoveryMode: "observe", Run: &run}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		claim, ok, err := s.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		report := &controlplane.RunRecoveryReport{CommandID: cmd.ID, RunID: run.ID, AgentID: "agent", QueuedAt: run.QueuedAt, Mode: "observe", TopologyUID: "topology", ObservedAt: time.Now().UTC(), Resources: []model.RecoveredResource{{ActionID: "action", Resource: "sysbox_node.node", OwnerUID: "topology", ResourceUID: "node", Generation: 1, Status: "unknown", JournalStatus: "uncertain"}}}
		_, err = s.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: cmd.ID, AgentID: "agent", Type: cmd.Type, Status: "started", LeaseOwner: claim.LeaseOwner, Attempt: claim.Attempt, Recovery: report})
		require.ErrorIs(t, err, persistence.ErrConflict)
		_, err = s.GetRunRecoveryReport(ctx, "agent", cmd.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		_, err = s.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: cmd.ID, AgentID: "agent", Type: cmd.Type, Status: "completed", LeaseOwner: claim.LeaseOwner, Attempt: claim.Attempt, Recovery: report})
		require.NoError(t, err)
		got, err := s.GetRunRecoveryReport(ctx, "agent", cmd.ID)
		require.NoError(t, err)
		require.Equal(t, *report, *got)
	})
}
