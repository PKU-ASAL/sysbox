//go:build e2e

package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerFenceRequiresExactTapAbsenceE2E(t *testing.T) {
	if os.Getenv("SYSBOX_ATTACHMENT_E2E") != "1" {
		t.Skip("requires enrolled isolated privileged execution host")
	}
	name := "sbfence-" + uuid.NewString()[:8]
	netID := substrate.OperationIdentity{OwnerUID: "fixture", TopologyUID: "topology", ResourceUID: name, Generation: 1, ActionID: "network"}
	netCtx := substrate.WithOperationIdentity(context.Background(), netID)
	spec := driver.IsolatedNetworkSpec{Name: name, Bridge: "br0", CIDR: "10.45.0.1/24"}
	require.NoError(t, (network.Driver{}).CreateIsolated(netCtx, spec))
	t.Cleanup(func() { require.NoError(t, (network.Driver{}).DeleteIsolated(netCtx, spec)) })
	s := &Substrate{rootfsDir: t.TempDir()}
	hs := &HandleState{TopologyUID: "topology", VMDir: filepath.Join(s.rootfsDir, "topology", "tap-vm", "1")}
	seedOwnedManifest(t, s, "tap-vm", hs, nil)
	h := firecrackerNodeHandle("tap-vm", hs)
	id := substrate.OperationIdentity{OwnerUID: hs.OwnerUID, TopologyUID: hs.TopologyUID, ResourceUID: hs.ResourceUID, Generation: hs.Generation, ActionID: "create"}
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	req := driver.AttachmentRequest{Name: "nic", NetworkIdentity: netID, IPPrefixes: []string{"10.45.0.2/24"}, NetworkState: json.RawMessage(`{"netns":"` + name + `","bridge":"br0"}`)}
	attached, err := s.Attach(ctx, h, req)
	require.NoError(t, err)
	var st attachmentState
	require.NoError(t, json.Unmarshal(attached.State, &st))
	require.NotNil(t, st.Link)
	alias, err := network.AttachmentAlias(id, req.Name)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, network.DeleteOwnedAttachment(ctx, *st.Link, alias)) })
	require.NoError(t, s.DestroyNode(ctx, h))
	require.NoError(t, s.HostFenceDeletionAbsent(ctx, h))
	require.Error(t, s.HostFenceAttachmentAbsent(ctx, h, req.Name, attached.State), "VMM directory absence does not prove TAP absence")
	require.NoError(t, network.DeleteOwnedAttachment(ctx, *st.Link, alias))
	require.NoError(t, s.HostFenceAttachmentAbsent(ctx, h, req.Name, attached.State))
	t.Log("real TAP: absent Firecracker generation retained link hold; exact owned link deletion discharged only attachment residue")
}
