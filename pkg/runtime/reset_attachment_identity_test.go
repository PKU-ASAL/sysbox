package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type resetIdentityNIC struct {
	driver.NIC
	seen []substrate.OperationIdentity
}

func (n *resetIdentityNIC) Delete(ctx context.Context, _ substrate.NodeHandle, _ driver.AttachmentRequest, _ json.RawMessage) error {
	id, _ := substrate.OperationIdentityFromContext(ctx)
	n.seen = append(n.seen, id)
	return nil
}

type resetIdentityDriver struct {
	driver.Reset
	seen []substrate.OperationIdentity
}

func (r *resetIdentityDriver) DestroyReset(ctx context.Context, _ substrate.ResetHandle) error {
	id, _ := substrate.OperationIdentityFromContext(ctx)
	r.seen = append(r.seen, id)
	return nil
}

func TestResetAttachmentDeletionUsesAuthorizedOldGeneration(t *testing.T) {
	for _, fault := range []string{"none", "owner", "resource", "generation", "handle", "missing"} {
		t.Run(fault, func(t *testing.T) {
			network := address.Resource("sysbox_network", "net")
			st := &state.State{Resources: []state.Resource{{Address: network}}}
			nic, reset := &resetIdentityNIC{}, &resetIdentityDriver{}
			current := &state.Resource{OwnerUID: "owner", ResourceUID: "node", Generation: 2, Attachments: []state.Attachment{{Name: "eth1", Network: network}}}
			item := &resetNodeContext{current: current, nicDriver: nic, resetDriver: reset, request: substrate.ResetRequest{Current: substrate.NodeHandle{ID: "old", Generation: 2}}}
			id := substrate.OperationIdentity{OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 3, ActionID: "reset"}
			switch fault {
			case "owner":
				id.OwnerUID = "foreign"
			case "resource":
				id.ResourceUID = "foreign"
			case "generation":
				id.Generation = 2
			case "handle":
				item.request.Current.Generation = 1
			}
			ctx := substrate.WithOperationIdentity(context.Background(), id)
			if fault == "missing" {
				ctx = context.Background()
			}
			err := NewExecutor(graph.New(), st, driver.DefaultRegistry).destroyResetNode(ctx, item)
			if fault != "none" {
				require.Error(t, err)
				require.Empty(t, nic.seen)
				require.Empty(t, reset.seen)
				return
			}
			require.NoError(t, err)
			old := id
			old.Generation = 2
			require.Equal(t, []substrate.OperationIdentity{old}, nic.seen)
			require.Equal(t, []substrate.OperationIdentity{id}, reset.seen)
		})
	}
}
