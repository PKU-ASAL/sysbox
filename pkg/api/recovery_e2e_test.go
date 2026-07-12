//go:build e2e
// +build e2e

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	fcprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
	netprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestCheckpointRecoverAndCleanupLocalNetworkE2E(t *testing.T) {
	requireRootE2E(t)

	nsName := "sysbox-e2e-recover-net"
	brName := "br-e2erecover"
	t.Cleanup(func() { _ = netprovider.DeleteNetns(nsName) })

	require.NoError(t, netprovider.CreateNetns(nsName))
	require.NoError(t, netprovider.CreateBridge(netprovider.BridgeConfig{
		NetnsName:  nsName,
		BridgeName: brName,
		CIDR:       "10.251.0.1/24",
	}))

	dir := t.TempDir()
	checkpointPath := filepath.Join(dir, "run.checkpoint.json")
	statePath := filepath.Join(dir, "state.json")
	cp := runtime.OperationCheckpoint{
		RunID:     "run-net",
		Topology:  "e2e-net",
		Operation: "apply",
		Status:    runtime.OperationFailed,
		Steps: []runtime.OperationStep{{
			Index:    0,
			Kind:     "resource",
			Resource: "sysbox_network.lan",
			Action:   controlplane.PlanActionCreate,
			Driver:   "network",
			Status:   runtime.OperationDone,
			StateResource: &runtime.StateResourceLog{
				Address: address.Resource("sysbox_network", "lan"),
				Driver:  "network",
				Attributes: map[string]any{
					"netns":   nsName,
					"bridge":  brName,
					"cidr":    "10.251.0.0/24",
					"gateway": "10.251.0.1/24",
				},
			},
		}},
	}
	writeCheckpointE2E(t, checkpointPath, cp)

	mgr := state.NewManager(statePath)
	store := &localAPIStore{runsDir: dir}
	require.NoError(t, store.SaveCheckpoint(context.Background(), "e2e-net", "run-net", cp))
	report, err := recoverCheckpoint(context.Background(), store, "e2e-net", "run-net", mgr, "e2e")
	require.NoError(t, err)
	require.Len(t, report.Recovered, 1)
	require.Equal(t, "recovered", report.Recovered[0].Status)

	st, err := mgr.LoadWithContext(context.Background())
	require.NoError(t, err)
	require.NotNil(t, st.FindResource(address.Resource("sysbox_network", "lan")))

	cleanup, err := cleanupCheckpoint(context.Background(), store, "e2e-net", "run-net")
	require.NoError(t, err)
	require.Len(t, cleanup.Networks, 1)
	require.Equal(t, "removed", cleanup.Networks[0].Status)
	require.False(t, netprovider.NetnsExists(nsName))
}

func TestCheckpointRecoverAndCleanupFirecrackerNodeE2E(t *testing.T) {
	requireRootE2E(t)
	registerFirecrackerForE2E(t)

	nsName := "sysbox-e2e-recover-fc"
	brName := "br-e2efc"
	tapName := "tap-e2efc"
	vmDir := filepath.Join(t.TempDir(), "vm")
	socketPath := filepath.Join(vmDir, "firecracker.sock")
	configPath := filepath.Join(vmDir, "vm_config.json")
	t.Cleanup(func() { _ = netprovider.DeleteNetns(nsName) })

	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0o644))
	require.NoError(t, netprovider.CreateNetns(nsName))
	require.NoError(t, netprovider.CreateBridge(netprovider.BridgeConfig{
		NetnsName:  nsName,
		BridgeName: brName,
		CIDR:       "10.252.0.1/24",
	}))
	require.NoError(t, netprovider.CreateTapInNetns(tapName, nsName, brName))
	require.True(t, netprovider.LinkExists(nsName, tapName))

	dir := t.TempDir()
	checkpointPath := filepath.Join(dir, "run.checkpoint.json")
	statePath := filepath.Join(dir, "state.json")
	cp := runtime.OperationCheckpoint{
		RunID:     "run-fc",
		Topology:  "e2e-fc",
		Operation: "apply",
		Status:    runtime.OperationFailed,
		Steps: []runtime.OperationStep{{
			Index:      0,
			Kind:       "resource",
			Resource:   "sysbox_node.microvm",
			Action:     controlplane.PlanActionCreate,
			Driver:     "firecracker",
			ExternalID: "sysbox-microvm",
			Status:     runtime.OperationDone,
			StateResource: &runtime.StateResourceLog{
				Type: "sysbox_node", Name: "microvm", Provider: "firecracker",
				Instance: map[string]any{
					"container_id": "sysbox-microvm",
					"primary_ip":   "10.252.0.20",
				},
				Private:     mustPrivateState(t, map[string]any{"vm_dir": vmDir, "socket": socketPath, "config_path": configPath, "netns_name": nsName}),
				Attachments: []state.Attachment{{Name: "internal", Node: address.Resource("sysbox_node", "microvm"), Network: address.Resource("sysbox_network", "internal"), MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.252.0.20/24"}, Driver: "firecracker", DriverState: json.RawMessage(fmt.Sprintf(`{"tap":%q,"netns":%q,"guest_device":"eth0"}`, tapName, nsName))}},
			},
		}},
	}
	writeCheckpointE2E(t, checkpointPath, cp)

	mgr := state.NewManager(statePath)
	store := &localAPIStore{runsDir: dir}
	require.NoError(t, store.SaveCheckpoint(context.Background(), "e2e-fc", "run-fc", cp))
	report, err := recoverCheckpoint(context.Background(), store, "e2e-fc", "run-fc", mgr, "e2e")
	require.NoError(t, err)
	require.Len(t, report.Recovered, 1)
	require.Equal(t, "recovered_not_running", report.Recovered[0].Status)

	st, err := mgr.LoadWithContext(context.Background())
	require.NoError(t, err)
	require.NotNil(t, st.FindResource(address.Resource("sysbox_node", "microvm")))

	cleanup, err := cleanupCheckpoint(context.Background(), store, "e2e-fc", "run-fc")
	require.NoError(t, err)
	require.Len(t, cleanup.MicroVMs, 1)
	require.Equal(t, "removed", cleanup.MicroVMs[0].Status)
	require.NoDirExists(t, vmDir)
	require.False(t, netprovider.LinkExists(nsName, tapName))
}

func mustPrivateState(t *testing.T, providerState any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(providerState)
	require.NoError(t, err)
	private, err := state.EncodePrivate(1, state.DriverPrivate{ProviderState: payload})
	require.NoError(t, err)
	return private
}

func requireRootE2E(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("checkpoint recovery e2e requires root/CAP_NET_ADMIN; run: sudo -E go test -tags e2e ./pkg/api")
	}
}

func registerFirecrackerForE2E(t *testing.T) {
	t.Helper()
	if _, ok := driver.DefaultRegistry.Get("firecracker"); ok {
		return
	}
	fc := fcprovider.New(filepath.Join(t.TempDir(), "vmlinux"), t.TempDir())
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "firecracker", Version: "e2e", Node: fc, NIC: fc, Artifact: fc, GuestExec: fc, Console: fc, Power: fc, NodeState: fc, GuestNetwork: fc}))
}

func writeCheckpointE2E(t *testing.T, path string, cp runtime.OperationCheckpoint) {
	t.Helper()
	data, err := json.MarshalIndent(cp, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}
