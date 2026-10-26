//go:build e2e

package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// A diskless VM exercises real libvirt identity/state without guest images or
// access to existing domains. No SSH success or reset acceptance is implied.
func TestLibvirtRealGenerationEffectsE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("set SYSBOX_LIBVIRT_E2E=1 and LIBVIRT_DEFAULT_URI for real libvirt acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := managedOperation()
	id.OwnerUID = uuid.NewString()
	id.ResourceUID = uuid.NewString()
	id.TopologyUID = uuid.NewString()
	sub := New(t.TempDir())
	name := "sysbox-fence-e2e-" + uuid.NewString()
	dir, err := sub.managedVMDir(id, name)
	require.NoError(t, err)
	hs := &HandleState{DomainName: name, DomainUUID: uuid.NewString(), Identity: &id, VMDir: dir, DiskPath: filepath.Join(dir, "disk.qcow2")}
	netID := id
	netID.ResourceUID = "network"
	suffix := uuid.NewString()[:8]
	networkSpec := driver.IsolatedNetworkSpec{Name: "sblv-" + suffix, Bridge: "br0", CIDR: "10.47.0.1/24", RootBridge: "br" + suffix, RootEnd: "vr" + suffix, NamespaceEnd: "vn" + suffix}
	netCtx := substrate.WithOperationIdentity(context.Background(), netID)
	require.NoError(t, (ownednetwork.Driver{}).CreateIsolated(netCtx, networkSpec))
	t.Cleanup(func() { require.NoError(t, (ownednetwork.Driver{}).DeleteIsolated(netCtx, networkSpec)) })
	hs.Bridges = []BridgeAttach{{Name: "lan", Bridge: networkSpec.RootBridge, Netns: networkSpec.Name, MAC: "02:00:00:00:47:02", Target: ownednetwork.BridgeTarget{Identity: netID, NetworkNamespace: networkSpec.Name, Bridge: networkSpec.RootBridge, Root: true}}}
	raw, err := GenerateDomainXML(DomainSpec{Name: name, UUID: hs.DomainUUID, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &id, ManagedState: hs})
	require.NoError(t, err)
	var domain domainXML
	require.NoError(t, xml.Unmarshal([]byte(raw), &domain))
	domain.Devices.Disks = nil
	domain.Devices.Interfaces = []domIface{{Type: "bridge", Source: domIfaceSrc{Bridge: networkSpec.RootBridge}, Model: domIfaceMod{Type: "virtio"}, MAC: &domIfaceMAC{Address: hs.Bridges[0].MAC}}}
	definition, err := xml.Marshal(domain)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, definition, 0600))
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "virsh", args...).CombinedOutput()
		require.NoError(t, err, "%s: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	run("define", path)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Only the random UUID defined by this test is eligible for cleanup.
		_, _ = exec.CommandContext(cleanup, "virsh", "destroy", hs.DomainUUID).CombinedOutput()
		_, _ = exec.CommandContext(cleanup, "virsh", "undefine", hs.DomainUUID).CombinedOutput()
	})
	run("start", hs.DomainUUID)
	h := managedNodeHandle(hs)
	old := id
	old.Generation--
	stale := substrate.WithOperationIdentity(ctx, old)
	for _, effect := range []struct {
		name string
		call func(context.Context, substrate.NodeHandle) error
	}{{"start", sub.StartNode}, {"stop", sub.StopNode}, {"pause", sub.Pause}, {"resume", sub.Resume}, {"destroy", sub.DestroyNode}} {
		err := effect.call(stale, h)
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%s: %v", effect.name, err)
		require.Equal(t, "running", run("domstate", h.ID))
	}
	// Also reject a self-consistent old handle against actual newer metadata.
	oldState := *hs
	oldState.Identity = &old
	err = sub.Pause(stale, managedNodeHandle(&oldState))
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
	require.Equal(t, "running", run("domstate", h.ID))
	current := substrate.WithOperationIdentity(ctx, id)
	targetRaw, err := json.Marshal(map[string]any{"netns": networkSpec.Name, "libvirt_bridge": networkSpec.RootBridge})
	require.NoError(t, err)
	request := driver.AttachmentRequest{Name: "lan", NetworkIdentity: netID, MAC: hs.Bridges[0].MAC, NetworkState: targetRaw}
	inventory, err := sub.DiscoverManagedAttachments(current, h, []driver.AttachmentRequest{request})
	require.NoError(t, err)
	require.Len(t, inventory, 1)
	_, err = sub.DiscoverManagedAttachments(current, h, nil)
	require.Error(t, err, "unaccounted real interface must block")
	wrong := request
	wrong.NetworkIdentity.Generation++
	_, err = sub.DiscoverManagedAttachments(current, h, []driver.AttachmentRequest{wrong})
	require.Error(t, err)
	require.NoError(t, sub.Pause(current, h))
	require.Equal(t, "paused", run("domstate", h.ID))
	observed, err := sub.ObserveNode(current, h)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusPaused, observed.Status)
	require.NoError(t, substrate.ValidateNodeObservationIdentity(id, observed))
	evidence, err := sub.InspectAdoption(current, h.ID, substrate.NodeSpec{})
	require.NoError(t, err)
	require.Equal(t, h.ID, evidence.ExternalID)
	adopted, err := sub.ApplyAdoption(current, evidence)
	require.NoError(t, err)
	require.Equal(t, h.ID, adopted.ID)
	observedAdopted, err := sub.ObserveAdoption(current, evidence)
	require.NoError(t, err)
	require.Equal(t, h.ID, observedAdopted.ID)
	recoveredState := observedAdopted.Provider.(*HandleState)
	require.Equal(t, hs.VMDir, recoveredState.VMDir)
	require.Equal(t, hs.DiskPath, recoveredState.DiskPath)
	encodedState, err := sub.MarshalProviderState(observedAdopted)
	require.NoError(t, err)
	cold := New(sub.vmDirRoot())
	decodedState, err := cold.UnmarshalProviderState(encodedState)
	require.NoError(t, err)
	coldHandle := substrate.NodeHandle{ID: observedAdopted.ID, Generation: observedAdopted.Generation, Provider: decodedState}
	observed, err = cold.ObserveNode(current, coldHandle)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusPaused, observed.Status)
	_, err = sub.ObserveNode(stale, managedNodeHandle(&oldState))
	require.Error(t, err, "actual newer domain metadata must reject self-consistent stale handle")
	require.NoError(t, cold.Resume(current, coldHandle))
	require.Equal(t, "running", run("domstate", h.ID))
	observed, err = sub.ObserveNode(current, h)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusRunning, observed.Status)
	require.NoError(t, substrate.ValidateNodeObservationIdentity(id, observed))
	// Preserve ownership metadata but inject an unproven inactive interface.
	// Start must reject before the daemon can attempt to use this target.
	run("destroy", h.ID)
	domain.Devices.Interfaces = append(domain.Devices.Interfaces, domIface{Type: "bridge", Source: domIfaceSrc{Bridge: "sb-unproven"}, Model: domIfaceMod{Type: "virtio"}})
	drifted, err := xml.Marshal(domain)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, drifted, 0600))
	run("define", path)
	err = sub.StartNode(current, h)
	require.ErrorContains(t, err, "interface count conflicts")
	require.Equal(t, "shut off", run("domstate", h.ID))
	require.NoError(t, sub.DestroyNode(current, h))
	out := run("list", "--all", "--uuid")
	require.NotContains(t, strings.Fields(out), h.ID)
	t.Log("real domain rejected stale context and stale handle; matching generation paused, resumed and destroyed")
}
