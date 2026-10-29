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
