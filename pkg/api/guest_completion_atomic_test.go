package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGuestCompletionRequiresCurrentLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "exec", CommandID: "cmd", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
		cmd := controlplane.AgentCommand{TopologyUID: "owner", ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Status: "queued", Protocol: controlplane.AgentProtocolVersion, Execution: &x, ExecutionRequest: x.Request, ActionID: x.ID, ResourceUID: "node", Generation: 2, LeaseEpoch: 1}
		require.NoError(t, s.CreateGuestExecutionDispatch(ctx, x, cmd))
		report := controlplane.GuestExecutionCompletion{Result: controlplane.GuestExecutionResult{ExitCode: 0}, ResultClass: controlplane.GuestExecutionResultClassExit, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, LeaseEpoch: cmd.LeaseEpoch}
		_, err := s.CompleteGuestExecution(ctx, x.AgentID, x.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
		old, ok, err := s.AcquireAgentCommandLease(ctx, x.AgentID, cmd.ID, "old", time.Nanosecond)
		require.NoError(t, err)
		require.True(t, ok)
		report.LeaseOwner, report.Attempt = old.LeaseOwner, old.Attempt
		_, err = s.CompleteGuestExecution(ctx, x.AgentID, x.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
		fresh, ok, err := s.AcquireAgentCommandLease(ctx, x.AgentID, cmd.ID, "new", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = s.CompleteGuestExecution(ctx, x.AgentID, x.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
		report.LeaseOwner, report.Attempt = fresh.LeaseOwner, fresh.Attempt
		completed, err := s.CompleteGuestExecution(ctx, x.AgentID, x.ID, report)
		require.NoError(t, err)
		require.Equal(t, int64(2), completed.Version)
		require.NoError(t, s.DeleteAgentCommand(ctx, x.AgentID, cmd.ID))
		repeated, err := s.CompleteGuestExecution(ctx, x.AgentID, x.ID, report)
		require.NoError(t, err)
		require.Equal(t, completed, repeated)
		report.Result.ExitCode = 1
		_, err = s.CompleteGuestExecution(ctx, x.AgentID, x.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}

func leaseFileCompletion(t *testing.T, s *Server, id string) controlplane.GuestFileOperationCompletion {
	t.Helper()
	op, err := s.apiStore.GetGuestFileOperation(context.Background(), id)
	require.NoError(t, err)
	lease, ok, err := s.apiStore.AcquireAgentCommandLease(context.Background(), op.AgentID, op.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	return controlplane.GuestFileOperationCompletion{ActionID: lease.ActionID, ResourceUID: lease.ResourceUID, Generation: lease.Generation, LeaseEpoch: lease.LeaseEpoch, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt}
}

func TestGuestFileCompletionChecksLeaseCancellationAndReceipt(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		for _, cancelled := range []bool{false, true} {
			id := "file"
			if cancelled {
				id = "cancelled"
			}
			ctx := context.Background()
			op := controlplane.GuestFileOperation{ID: id, CommandID: "cmd-" + id, AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued}
			cmd := controlplane.AgentCommand{TopologyUID: "owner", ID: op.CommandID, AgentID: op.AgentID, Type: "guest_file_put", Status: "queued", Protocol: controlplane.AgentProtocolVersion, ActionID: id, ResourceUID: "node", Generation: 2, LeaseEpoch: 1, FilePut: &controlplane.GuestFilePut{ID: id, Topology: op.Topology, Node: op.Node}}
			require.NoError(t, s.CreateGuestFileDispatch(ctx, op, cmd))
			lease, ok, err := s.AcquireAgentCommandLease(ctx, op.AgentID, cmd.ID, "worker", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			report := controlplane.GuestFileOperationCompletion{ActionID: lease.ActionID, ResourceUID: lease.ResourceUID, Generation: lease.Generation, LeaseEpoch: lease.LeaseEpoch, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt}
			_, err = s.CompleteGuestFileOperation(ctx, op.AgentID, id, report)
			require.ErrorIs(t, err, persistence.ErrConflict, "completion before running rejected")
			updated, err := newGuestFileOperationService(s).MarkRunning(ctx, id)
			require.NoError(t, err)
			if cancelled {
				_, err = newGuestFileOperationService(s).RequestCancel(ctx, id)
				require.NoError(t, err)
				_, err = s.CompleteGuestFileOperation(ctx, op.AgentID, id, report)
				require.ErrorIs(t, err, persistence.ErrConflict)
				continue
			}
			stale := report
			stale.Attempt++
			_, err = s.CompleteGuestFileOperation(ctx, op.AgentID, id, stale)
			require.ErrorIs(t, err, persistence.ErrConflict)
			_, err = s.CompleteGuestFileOperation(ctx, op.AgentID, id, report)
			require.ErrorIs(t, err, persistence.ErrConflict, "worker proof cannot attest installation quiescence")
			unchanged, err := s.GetGuestFileOperation(ctx, id)
			require.NoError(t, err)
			require.Equal(t, updated.Version, unchanged.Version)
			require.NoError(t, s.DeleteAgentCommand(ctx, op.AgentID, cmd.ID))
			_, err = s.CompleteGuestFileOperation(ctx, op.AgentID, id, report)
			require.ErrorIs(t, err, persistence.ErrConflict)
			report.Error = "late different error"
			_, err = s.CompleteGuestFileOperation(ctx, op.AgentID, id, report)
			require.ErrorIs(t, err, persistence.ErrConflict)
		}
	})
}
