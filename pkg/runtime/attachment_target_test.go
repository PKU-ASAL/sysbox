package runtime

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAttachmentCarriesNetworkIdentityOnCreateAndRecovery(t *testing.T) {
	network := state.Resource{Address: address.Resource("sysbox_network", "lab"), OwnerUID: "topology", ResourceUID: "network", Generation: 4, Attributes: state.MustAttributes(map[string]any{"netns": "lab", "bridge": "br0"})}
	st := &state.State{Resources: []state.Resource{network}}
	nodeID := substrate.OperationIdentity{ActionID: "node-action", OwnerUID: "topology", TopologyUID: "topology", ResourceUID: "node", Generation: 9}
	result, err := wireNICs(substrate.WithOperationIdentity(context.Background(), nodeID), hookSubstrate{}, st, substrate.NodeHandle{ID: "node"}, []NICSpec{{Name: "nic", Network: "lab"}}, address.Resource("sysbox_node", "node"))
	require.NoError(t, err)
	request := result.Requests["nic"]
	require.Equal(t, "network", request.NetworkIdentity.ResourceUID)
	require.Equal(t, int64(4), request.NetworkIdentity.Generation)
	require.NoError(t, request.NetworkIdentity.Validate())
	recovered, err := attachmentRequestFromState(st, result.Attachments[0])
	require.NoError(t, err)
	require.Equal(t, request.NetworkIdentity, recovered.NetworkIdentity)
}
