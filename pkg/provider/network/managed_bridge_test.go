package network

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestManagedBridgeProofCannotChangeNetworkIdentity(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "network", Generation: 1}
	proof := substrate.ManagedBridgeTarget{Version: 1, Provider: "docker", NetworkID: "network-id", Identity: id, Bridge: "explicit-br", Device: 1, Inode: 2, Index: 3, Alias: "owned"}
	target := BridgeTarget{Identity: id, Bridge: proof.Bridge, Managed: &proof}
	target.Identity.Generation++
	_, err := (Driver{}).bridgeTargetProof(context.Background(), target)
	require.ErrorContains(t, err, "generation")
}
