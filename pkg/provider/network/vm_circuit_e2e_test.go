//go:build e2e

package network

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestVMFabricSeparateNetworksE2E(t *testing.T) {
	if os.Getenv("SYSBOX_VM_FABRIC_E2E") != "1" {
		t.Skip("isolated disposable container only")
	}
	ctx := context.Background()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: uuid.NewString(), Generation: 1}
	ns, err := NewVMNamespace(ctx, id)
	require.NoError(t, err)
	require.NoError(t, ConfigureVMNamespace(ctx, ns))
	var circuits []VMCircuit
	var targets []BridgeTarget
	var specs []driver.IsolatedNetworkSpec
	for i, name := range []string{"one", "two"} {
		nid := id
		nid.ResourceUID = uuid.NewString()
		spec := driver.IsolatedNetworkSpec{Name: "sbtest-" + nid.ResourceUID[:8], Bridge: "br0", CIDR: []string{"10.91.0.1/24", "10.91.0.2/24"}[i]}
		require.NoError(t, (Driver{}).CreateIsolated(substrate.WithOperationIdentity(ctx, nid), spec))
		target := BridgeTarget{Identity: nid, NetworkNamespace: spec.Name, Bridge: spec.Bridge}
		b, err := OpenOwnedBridge(ctx, target)
		require.NoError(t, err)
		alias, err := AttachmentAlias(id, name)
		require.NoError(t, err)
		p, err := b.PrepareVMCircuit(ctx, ns, alias)
		require.NoError(t, err)
		checkpoints := 0
		err = b.CreateVMCircuit(ctx, ns, &p, func(VMCircuit) error { checkpoints++; return nil })
		b.Close()
		require.NoError(t, err)
		require.Positive(t, checkpoints)
		circuits = append(circuits, p)
		targets = append(targets, target)
		specs = append(specs, spec)
	}
	require.NotEqual(t, circuits[0].Bridge.Index, circuits[1].Bridge.Index)
	bridges, unlock, err := LockBridgeTargets(ctx, targets)
	require.NoError(t, err)
	require.NoError(t, VerifyVMCircuitInventory(ctx, ns, circuits))
	for i, p := range circuits {
		require.NoError(t, bridges[i].ObserveVMCircuit(ctx, ns, p))
		require.NoError(t, bridges[i].SetVMCircuitTapOwner(ctx, ns, p, 32001))
		require.NoError(t, bridges[i].SetVMCircuitTapOwner(ctx, ns, p, 0))
	}
	unlock()
	// Both network endpoints have on-link addresses in the same subnet. If the
	// VM fabric accidentally bridges circuits, ARP and this ping would succeed.
	_, err = exec.CommandContext(ctx, "ip", "netns", "exec", specs[0].Name, "ping", "-c", "1", "-W", "1", "10.91.0.2").CombinedOutput()
	require.Error(t, err, "independent networks were accidentally bridged")
	require.Error(t, DeleteVMNamespace(ctx, ns), "namespace still has circuit devices")
	for _, p := range circuits {
		require.NoError(t, DeleteVMCircuit(ctx, ns, p))
		require.NoError(t, DeleteVMCircuit(ctx, ns, p))
		require.NoError(t, VMCircuitAbsent(ctx, ns, p))
	}
	require.NoError(t, DeleteVMNamespace(ctx, ns))
	require.NoError(t, DeleteVMNamespace(ctx, ns))
	for i, spec := range specs {
		require.NoError(t, (Driver{}).DeleteIsolated(substrate.WithOperationIdentity(ctx, targets[i].Identity), spec))
	}
}
