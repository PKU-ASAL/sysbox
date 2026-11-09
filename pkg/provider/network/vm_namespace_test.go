package network

import (
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestVMNamespaceProofBindsGeneration(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "create", TopologyUID: "top", OwnerUID: "top", ResourceUID: "vm", Generation: 1}
	proof := VMNamespace{Version: 1, Name: VMNamespaceName(id), Identity: id, Device: 1, Inode: 2}
	require.NoError(t, proof.Validate(id))
	other := id
	other.Generation++
	require.Error(t, proof.Validate(other))
	proof.Inode = 0
	require.Error(t, proof.Validate(id))
}
func TestVMNetworkNamesSeparateAttachmentsAndGenerations(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "create", TopologyUID: "top", OwnerUID: "top", ResourceUID: "vm", Generation: 1}
	first := VMNamespaceName(id)
	id.Generation++
	require.NotEqual(t, first, VMNamespaceName(id))
	a, _ := AttachmentAlias(id, "one")
	b, _ := AttachmentAlias(id, "two")
	require.NotEqual(t, vmCircuitName("b", a), vmCircuitName("b", b))
	for _, prefix := range []string{"b", "v", "p", "t"} {
		require.LessOrEqual(t, len(vmCircuitName(prefix, a)), 15)
	}
}
