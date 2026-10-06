package network

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAttachmentIdentityRejectsMissingOrStaleProof(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "attach", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "node", Generation: 2}
	alias, err := AttachmentAlias(id, "nic")
	require.NoError(t, err)
	require.Error(t, DeleteOwnedAttachment(context.Background(), AttachmentLink{}, alias))
	id.Generation++
	newAlias, err := AttachmentAlias(id, "nic")
	require.NoError(t, err)
	require.NotEqual(t, alias, newAlias)
	require.Error(t, DeleteOwnedAttachment(context.Background(), AttachmentLink{Alias: alias, Name: "foreign", Namespace: "foreign", Kind: "veth", Index: 1, Inode: 1}, newAlias))
	_, err = AttachmentAlias(substrate.OperationIdentity{}, "nic")
	require.Error(t, err)
}
