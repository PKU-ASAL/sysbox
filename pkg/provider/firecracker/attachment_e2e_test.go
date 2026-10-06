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
	require.NoError(t, network.CreateNetns(name))
	t.Cleanup(func() { require.NoError(t, network.DeleteNetns(name)) })
	require.NoError(t, network.CreateBridge(network.BridgeConfig{NetnsName: name, BridgeName: "br0", CIDR: "10.44.0.1/24"}))
	s := &Substrate{rootfsDir: t.TempDir()}
	id := "tap-vm"
	hs := &HandleState{TopologyUID: "topology", VMDir: filepath.Join(s.rootfsDir, "topology", id, "1")}
	seedOwnedManifest(t, s, id, hs, nil)
	h := firecrackerNodeHandle(id, hs)
	ctx := context.Background()
	request := driver.AttachmentRequest{Name: "nic", IPPrefixes: []string{"10.44.0.2/24"}, NetworkState: []byte(`{"netns":"` + name + `","bridge":"br0"}`)}
	attached, err := s.Attach(ctx, h, request)
	require.NoError(t, err)
	_, err = s.Observe(ctx, h, request, attached.State)
	require.NoError(t, err)
	stale := substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "stale", OwnerUID: hs.OwnerUID, ResourceUID: hs.ResourceUID, TopologyUID: hs.TopologyUID, Generation: 2})
	require.Error(t, s.Delete(stale, h, request, attached.State))
	require.NoError(t, s.Delete(ctx, h, request, attached.State))
	require.NoError(t, s.Delete(ctx, h, request, attached.State))
	attached, err = s.Attach(ctx, h, request)
	require.NoError(t, err)
	tap := attachmentTapName(id, request.Name)
	for _, args := range [][]string{{"netns", "exec", name, ipBin, "link", "del", tap}, {"netns", "exec", name, ipBin, "tuntap", "add", "dev", tap, "mode", "tap"}} {
		out, err := exec.Command(ipBin, args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.Error(t, s.Delete(ctx, h, request, attached.State), "same-name foreign TAP must survive")
	out, err := exec.Command(ipBin, "netns", "exec", name, ipBin, "link", "show", tap).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Log("real Firecracker cold-plug TAP ownership, stale deletion and same-name replacement rejection passed; no VM was booted")
}
