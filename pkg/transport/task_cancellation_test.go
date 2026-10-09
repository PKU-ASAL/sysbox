package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

type cancellationCaller struct {
	calls  []guesttask.Call
	record guesttask.Record
}

func (c *cancellationCaller) taskCall(_ context.Context, call guesttask.Call) (guesttask.Response, error) {
	c.calls = append(c.calls, call)
	return guesttask.Response{Version: guesttask.Version, Record: &c.record}, nil
}

func TestTaskCancellationForwardsOnlyMatchingStopAuthority(t *testing.T) {
	h := substrate.ExecutionHandle{ID: "task", Digest: "digest", BootID: "boot", Epoch: 2, NodeID: "node", TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
	g := controlplane.ExecutionGrant{ActionID: h.ID, Digest: h.Digest, TargetBootID: h.BootID, Epoch: h.Epoch, Scope: controlplane.ExecutionScope{TopologyUID: h.TopologyUID, ResourceUID: h.ResourceUID, Generation: 1}, State: controlplane.GrantRevoked}
	proof := controlplane.SignedExecutionCancellation{Grant: g, Signature: []byte("wire-proof")}
	ctx := WithTaskCancellation(context.Background(), proof)
	_, _, canStart := TaskGrantFromContext(ctx)
	require.False(t, canStart)
	caller := &cancellationCaller{record: guesttask.Record{ID: h.ID, Digest: h.Digest, BootID: h.BootID, Grant: guesttask.SignedGrant{Grant: g}, Status: "cancelled", CancelRequested: true, TerminationConfirmed: true}}
	stopped, err := cancelTask(ctx, caller, executionIdentity{nodeID: h.NodeID, generation: 1}, h)
	require.NoError(t, err)
	require.Equal(t, substrate.RemoteTerminationConfirmed, stopped)
	require.Equal(t, &proof, caller.calls[0].Cancellation)
	result, err := observeTask(ctx, caller, executionIdentity{nodeID: h.NodeID, generation: 1}, h)
	require.NoError(t, err)
	require.Equal(t, substrate.ExecErrorCanceled, result.ErrorClass)
	require.Nil(t, caller.calls[1].Cancellation)
	bad := proof
	bad.Grant.Scope.ResourceUID = "other"
	_, err = cancelTask(WithTaskCancellation(context.Background(), bad), caller, executionIdentity{nodeID: h.NodeID, generation: 1}, h)
	require.ErrorIs(t, err, guesttask.ErrConflict)
	require.Len(t, caller.calls, 2)
}
