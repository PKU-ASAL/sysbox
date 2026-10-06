package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestNodeOperationCompletionRequiresCurrentCommandLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		op, cmd := nodeDispatchFixture()
		require.NoError(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
		report := op
		report.Status = controlplane.NodeOperationStatusDone
		report.EndedAt = time.Now().UTC()
		_, err := s.CompleteNodeOperation(ctx, op.AgentID, op.ID, report)
		require.Error(t, err)
		lease, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		report.LeaseOwner = lease.LeaseOwner
		report.Attempt = lease.Attempt
		report.LeaseEpoch = lease.LeaseEpoch
		stale := report
		stale.Attempt--
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, stale)
		require.Error(t, err)
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, report)
		require.NoError(t, err)
		require.NoError(t, s.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID))
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, report)
		require.NoError(t, err, "identical final response remains idempotent after command retirement")
		changed := report
		changed.Err = "changed"
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, changed)
		require.Error(t, err)
	})
}

func TestNodeOperationCompletionRejectsExpiredAndReplacedLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		op, cmd := nodeDispatchFixture()
		require.NoError(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
		old, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "old-worker", time.Nanosecond)
		require.NoError(t, err)
		require.True(t, ok)
		report := op
		report.Status = controlplane.NodeOperationStatusDone
		report.EndedAt = time.Now().UTC()
		report.LeaseOwner = old.LeaseOwner
		report.Attempt = old.Attempt
		report.LeaseEpoch = old.LeaseEpoch
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, report)
		require.Error(t, err)
		fresh, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "new-worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, report)
		require.Error(t, err)
		stored, err := s.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.NodeOperationStatusQueued, stored.Status)
		report.LeaseOwner = fresh.LeaseOwner
		report.Attempt = fresh.Attempt
		report.LeaseEpoch = fresh.LeaseEpoch
		_, err = s.CompleteNodeOperation(ctx, op.AgentID, op.ID, report)
		require.NoError(t, err)
	})
}
