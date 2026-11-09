package firecracker

import (
	"context"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFabricUnresolvedNamespaceCannotCertifyDeletion(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	m := &vmManifest{OwnerUID: id.OwnerUID, TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: id.Generation, Handle: HandleState{FabricPending: network.VMNamespaceName(id)}}
	require.ErrorContains(t, cleanupFabric(context.Background(), m), "outcome unknown")
}
func TestFabricProofDoesNotFallBackToLegacy(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	ns := network.VMNamespace{Version: 1, Name: network.VMNamespaceName(id), Identity: id, Device: 1, Inode: 2}
	h := HandleState{Version: manifestVersion, Fabric: &ns, NetnsName: ns.Name}
	require.NoError(t, validateFabricHandle(h, id))
	h.Attachments = []managedAttachment{{Name: "one"}}
	require.Error(t, validateFabricHandle(h, id))
	h.Fabric = nil
	h.Attachments[0].Circuit = &network.VMCircuit{}
	require.Error(t, validateFabricHandle(h, id))
	require.False(t, supportedNetworkHandle(HandleState{Version: 3, Fabric: &ns}))
	require.True(t, supportedNetworkHandle(HandleState{Version: 3}))
}
func TestFabricCloneKeepsGenerationEvidenceIndependent(t *testing.T) {
	h := &HandleState{Fabric: &network.VMNamespace{Inode: 12}, Attachments: []managedAttachment{{Circuit: &network.VMCircuit{Alias: "old"}}}}
	copy := cloneHandleState(h)
	copy.Fabric.Inode = 13
	copy.Attachments[0].Circuit.Alias = "new"
	require.EqualValues(t, 12, h.Fabric.Inode)
	require.Equal(t, "old", h.Attachments[0].Circuit.Alias)
}
func TestFabricRejectsSSHInsteadOfUsingUnaddressedNamespace(t *testing.T) {
	h := substrate.NodeHandle{Provider: &HandleState{Fabric: &network.VMNamespace{}, NetnsName: "fabric"}}
	_, err := (&Substrate{}).Connection(h, []substrate.ConnectionHint{{Type: "ssh"}})
	require.ErrorContains(t, err, "requires vsock")
}
