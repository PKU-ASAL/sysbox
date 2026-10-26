//go:build e2e

package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// Diskless real domain: proves NAT bridge attachment/start/retry/observation,
// not guest IP configuration or outbound traffic.
func TestLibvirtDockerNATLifecycleE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" || os.Getenv("SYSBOX_MANAGED_BRIDGE_E2E") != "1" {
		t.Skip("requires isolated libvirt and Docker network host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	docker, err := dockerprovider.New()
	require.NoError(t, err)
	defer docker.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Network: docker}))
	id := managedOperation()
	id.OwnerUID = uuid.NewString()
	id.TopologyUID = id.OwnerUID
	id.ResourceUID = "node"
	netID := id
	netID.ResourceUID = "network"
	netCtx := substrate.WithOperationIdentity(ctx, netID)
	info, err := docker.CreateManagedNetwork(netCtx, substrate.ManagedNetworkSpec{Name: "sysbox-lv-nat-" + uuid.NewString(), CIDR: fmt.Sprintf("10.228.%d.0/24", time.Now().UnixNano()%200+20), NAT: true, ExposeBridge: true})
	require.NoError(t, err)
	require.NotNil(t, info.BridgeTarget)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		require.NoError(t, docker.RemoveManagedNetwork(substrate.WithOperationIdentity(cleanup, netID), info.ID))
	})
	sub := New(t.TempDir())
	hs := &HandleState{Identity: &id, DomainName: "sysbox-lv-nat-" + uuid.NewString(), DomainUUID: uuid.NewString(), ExternalDefinition: true, AdoptionFingerprint: "test", NetworkInit: substrate.GuestNetworkInitPreconfigured}
	h := managedNodeHandle(hs)
	nodeCtx := substrate.WithOperationIdentity(ctx, id)
	raw, err := json.Marshal(map[string]any{"nat": true, "docker_network_id": info.ID, "managed_bridge": info.BridgeTarget})
	require.NoError(t, err)
	req := driver.AttachmentRequest{Name: "nat", NetworkIdentity: netID, MAC: "02:00:00:00:48:02", NetworkState: raw}
	attached, err := sub.Attach(nodeCtx, h, req)
	require.NoError(t, err)
	again, err := sub.Attach(nodeCtx, h, req)
	require.NoError(t, err)
	require.Equal(t, attached, again)
	require.Len(t, hs.Bridges, 1)
	wrong := req
	wrong.NetworkIdentity.Generation++
	_, err = sub.Attach(nodeCtx, h, wrong)
	require.Error(t, err)
	require.Len(t, hs.Bridges, 1)
	text, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: hs.DomainUUID, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &id, ManagedState: hs, Bridges: hs.Bridges})
	require.NoError(t, err)
	var domain domainXML
	require.NoError(t, xml.Unmarshal([]byte(text), &domain))
	domain.Devices.Disks = nil
	definition, err := xml.Marshal(domain)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, definition, 0600))
	run := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "virsh", args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("define", path)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = exec.CommandContext(cleanup, "virsh", "destroy", hs.DomainUUID).CombinedOutput()
		out, err := exec.CommandContext(cleanup, "virsh", "undefine", hs.DomainUUID).CombinedOutput()
		require.NoError(t, err, string(out))
	})
	run("start", hs.DomainUUID)
	_, err = sub.Observe(nodeCtx, h, req, attached.State)
	require.NoError(t, err)
	_, err = sub.Observe(nodeCtx, h, wrong, attached.State)
	require.Error(t, err)
	found, err := sub.DiscoverManagedAttachments(nodeCtx, h, []driver.AttachmentRequest{req})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.NoError(t, sub.StopNode(nodeCtx, h))
	require.NoError(t, sub.StartNode(nodeCtx, h))
	_, err = sub.Observe(nodeCtx, h, req, attached.State)
	require.NoError(t, err)
	require.NoError(t, sub.StopNode(nodeCtx, h))
	t.Log("real Docker NAT target, single libvirt interface, retry, stale target refusal, cold start validation and complete inventory passed")
}
