package network

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBridgeTargetRejectsMissingOldAndConflictingAnchor(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	id, _ := substrate.OperationIdentityFromContext(ctx)
	target := BridgeTarget{Identity: id, NetworkNamespace: spec.Name, Bridge: spec.Bridge}
	_, err := d.bridgeTargetProof(context.Background(), target)
	require.ErrorContains(t, err, "rebuild")
	for _, version := range []int{1, 2} {
		marker := networkOwnership{Version: version, Identity: id, Spec: spec}
		require.NoError(t, d.writeOwnership(spec.Name, marker))
		_, err = d.bridgeTargetProof(ctx, target)
		require.Error(t, err, "old or incomplete anchor must not authorize effects")
	}
	marker := networkOwnership{Version: 2, Identity: id, Spec: spec, NamespaceDevice: 7, NamespaceInode: 8,
		Links: []AttachmentLink{{Version: 1, Namespace: spec.Name, Device: 7, Inode: 8, Name: spec.Bridge, Index: 42, Kind: "bridge", Alias: networkAlias(id)}}}
	require.NoError(t, d.writeOwnership(spec.Name, marker))
	proof, err := d.bridgeTargetProof(ctx, target)
	require.NoError(t, err)
	require.Equal(t, 42, proof.Index)
	target.Identity.Generation++
	_, err = d.bridgeTargetProof(ctx, target)
	require.Error(t, err)
	target.Identity = id
	target.Bridge = "foreign"
	_, err = d.bridgeTargetProof(ctx, target)
	require.Error(t, err)
	target.NetworkNamespace = "../escape"
	_, err = d.bridgeTargetProof(ctx, target)
	require.Error(t, err)
}
