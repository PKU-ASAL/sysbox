package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestLocalNodeCompletionPreservesTransitionFailure(t *testing.T) {
	ctx := context.Background()
	s := &localAPIStore{runsDir: t.TempDir()}
	op, cmd := nodeDispatchFixture()
	require.NoError(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
	failure := errors.New("injected transition rejection")
	result, err := s.completeNodeOperation(ctx, op.AgentID, op.ID, nil, func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return op, failure
	})
	require.ErrorIs(t, err, failure)
	require.Nil(t, result)
	stored, err := s.GetNodeOperation(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, op, *stored)
}

func TestLocalHostNodeCompletionFailureDoesNotReleaseAuthority(t *testing.T) {
	ctx := context.Background()
	s := &localAPIStore{runsDir: t.TempDir()}
	op, cmd := nodeDispatchFixture()
	require.NoError(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s})
	session, err := authority.OpenSession(ctx, op.AgentID, "host", "")
	require.NoError(t, err)
	g, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: op.ResourceUID, Generation: op.Generation}, AgentID: op.AgentID, SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: 1, Operation: "ssh_access_apply", Digest: "digest", TargetBootID: "host"}, time.Minute)
	require.NoError(t, err)
	receipt := controlplane.SignedExecutionStopReceipt{Grant: *g}
	failure := errors.New("rejected host transition")
	result, err := s.completeNodeOperation(ctx, op.AgentID, op.ID, &receipt, func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return op, failure
	})
	require.ErrorIs(t, err, failure)
	require.Nil(t, result)
	held, err := authority.InspectGrant(ctx, op.AgentID, g.Scope)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantActive, held.State)
	require.Empty(t, held.StopReceipt)
	stored, err := s.GetNodeOperation(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, op, *stored)
}

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
