//go:build e2e

package firecracker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerOwnedTapE2E(t *testing.T) {
	if os.Getenv("SYSBOX_ATTACHMENT_E2E") != "1" {
		t.Skip("requires isolated container with TUN and namespace privileges")
	}
	name := "sbtap-" + uuid.NewString()[:8]
	networkID := substrate.OperationIdentity{OwnerUID: "owner", TopologyUID: "topology", ResourceUID: name, Generation: 2, ActionID: "create"}
	networkCtx := substrate.WithOperationIdentity(context.Background(), networkID)
	networkSpec := driver.IsolatedNetworkSpec{Name: name, Bridge: "br0", CIDR: "10.44.0.1/24"}
	require.NoError(t, (network.Driver{}).CreateIsolated(networkCtx, networkSpec))
	t.Cleanup(func() { require.NoError(t, (network.Driver{}).DeleteIsolated(networkCtx, networkSpec)) })
	s := &Substrate{rootfsDir: t.TempDir()}
	id := "tap-vm"
	hs := &HandleState{TopologyUID: "topology", VMDir: filepath.Join(s.rootfsDir, "topology", id, "1")}
	seedOwnedManifest(t, s, id, hs, nil)
	h := firecrackerNodeHandle(id, hs)
	ctx := context.Background()
	request := driver.AttachmentRequest{NetworkIdentity: networkID, Name: "nic", IPPrefixes: []string{"10.44.0.2/24"}, NetworkState: []byte(`{"netns":"` + name + `","bridge":"br0"}`)}
	staleTarget := request
	staleTarget.NetworkIdentity.Generation--
	_, err := s.Attach(ctx, h, staleTarget)
	require.Error(t, err)
	_, err = exec.Command(ipBin, "link", "show", attachmentTapName(id, request.Name)).CombinedOutput()
	require.Error(t, err, "invalid target must not create a TAP")
	attached, err := s.Attach(ctx, h, request)
	require.NoError(t, err)
	_, err = s.Observe(ctx, h, request, attached.State)
	require.NoError(t, err)
	m, err := s.loadManifest(ctx, h)
	require.NoError(t, err)
	unlock, err := lockStartAttachments(ctx, m)
	require.NoError(t, err)
	unlock()
	tap := attachmentTapName(id, request.Name)
	out, err := exec.Command(ipBin, "netns", "exec", name, ipBin, "link", "set", tap, "nomaster").CombinedOutput()
	require.NoError(t, err, string(out))
	_, err = lockStartAttachments(ctx, m)
	require.ErrorContains(t, err, "bridge drifted")
	attached, err = s.Attach(ctx, h, request)
	require.NoError(t, err)
	require.Equal(t, 1, hs.NICCount, "retry must not append another interface")
	stale := substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "stale", OwnerUID: hs.OwnerUID, ResourceUID: hs.ResourceUID, TopologyUID: hs.TopologyUID, Generation: 2})
	require.Error(t, s.Delete(stale, h, request, attached.State))
	require.NoError(t, s.Delete(ctx, h, request, attached.State))
	require.NoError(t, s.Delete(ctx, h, request, attached.State))
	attached, err = s.Attach(ctx, h, request)
	require.NoError(t, err)
	for _, args := range [][]string{{"netns", "exec", name, ipBin, "link", "del", tap}, {"netns", "exec", name, ipBin, "tuntap", "add", "dev", tap, "mode", "tap"}} {
		out, err := exec.Command(ipBin, args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.Error(t, s.Delete(ctx, h, request, attached.State), "same-name foreign TAP must survive")
	m, err = s.loadManifest(ctx, h)
	require.NoError(t, err)
	_, err = lockStartAttachments(ctx, m)
	require.ErrorContains(t, err, "attachment link replaced")
	out, err = exec.Command(ipBin, "netns", "exec", name, ipBin, "link", "show", tap).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Log("real Firecracker cold-plug TAP ownership, stale deletion and same-name replacement rejection passed; no VM was booted")
}
