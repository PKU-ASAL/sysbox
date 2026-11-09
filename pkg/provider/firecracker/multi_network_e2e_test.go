//go:build e2e

package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerMultiNetworkLifecycleE2E(t *testing.T) {
	if os.Getenv("SYSBOX_VM_FABRIC_E2E") != "1" {
		t.Skip("disposable isolated network container only")
	}
	s := &Substrate{rootfsDir: t.TempDir()}
	hs0 := &HandleState{TopologyUID: "topology", VMDir: filepath.Join(s.rootfsDir, "topology", "fabric-vm", "1")}
	seedOwnedManifest(t, s, "fabric-vm", hs0, nil)
	h := firecrackerNodeHandle("fabric-vm", hs0)
	ctx := context.Background()
	var requests []driver.AttachmentRequest
	var attachments []driver.AttachmentResult
	var specs []driver.IsolatedNetworkSpec
	for i, name := range []string{"front", "back"} {
		id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: uuid.NewString(), Generation: 1}
		spec := driver.IsolatedNetworkSpec{Name: "sbfabric-" + id.ResourceUID[:8], Bridge: "br0", CIDR: []string{"10.92.1.1/24", "10.92.2.1/24"}[i]}
		require.NoError(t, (network.Driver{}).CreateIsolated(substrate.WithOperationIdentity(ctx, id), spec))
		raw, _ := json.Marshal(networkState{NetNS: spec.Name, Bridge: "br0"})
		req := driver.AttachmentRequest{Name: name, NetworkIdentity: id, NetworkState: raw, MAC: []string{"02:00:00:00:01:01", "02:00:00:00:02:01"}[i], IPPrefixes: []string{[]string{"10.92.1.2/24", "10.92.2.2/24"}[i]}}
		result, err := s.Attach(ctx, h, req)
		require.NoError(t, err)
		requests = append(requests, req)
		attachments = append(attachments, result)
		specs = append(specs, spec)
	}
	hs := h.Provider.(*HandleState)
	require.NotNil(t, hs.Fabric)
	require.Len(t, hs.Attachments, 2)
	require.NotEqual(t, hs.NetnsName, specs[0].Name)
	require.NotEqual(t, hs.NetnsName, specs[1].Name)
	cold := &Substrate{rootfsDir: s.rootfsDir}
	for i, req := range requests {
		_, err := cold.Observe(ctx, h, req, attachments[i].State)
		require.NoError(t, err)
	}
	discovered, err := cold.DiscoverManagedAttachments(ctx, h, requests)
	require.NoError(t, err)
	require.Len(t, discovered, 2)
	manifest, err := cold.loadManifest(ctx, h)
	require.NoError(t, err)
	unlock, err := lockStartAttachmentsForUID(ctx, manifest, 32001)
	require.NoError(t, err)
	unlock()
	require.NoError(t, revokeJailTapAccess(manifest))
	bad := requests[0]
	bad.NetworkIdentity.Generation++
	_, err = cold.Observe(ctx, h, bad, attachments[0].State)
	require.Error(t, err)
	for i, req := range requests {
		require.NoError(t, cold.Delete(ctx, h, req, attachments[i].State))
		require.NoError(t, cold.Delete(ctx, h, req, attachments[i].State))
	}
	require.NoError(t, cold.DestroyNode(ctx, h))
	require.NoError(t, network.VMNamespaceAbsent(ctx, *hs.Fabric))
	for i, spec := range specs {
		require.NoError(t, (network.Driver{}).DeleteIsolated(substrate.WithOperationIdentity(context.Background(), requests[i].NetworkIdentity), spec))
	}
}

func TestFirecrackerMultiNetworkRealBootE2E(t *testing.T) {
	if os.Getenv("SYSBOX_VM_FABRIC_KVM_E2E") != "1" {
		t.Skip("explicit disposable KVM container only")
	}
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	s.jailerBin = "/usr/local/bin/jailer"
	s.cgroupParent = "sysbox-fabric-test"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "multi-vm", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	digest, err := digestFile(rootfs)
	require.NoError(t, err)
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactRootFS, Source: rootfs, Digest: digest, Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	node := substrate.NodeSpec{Name: "multi-vm", Image: substrate.ArtifactHandle{ID: rootfs, Identity: baseline}, ProviderConfig: &Config{Kernel: kernel}}
	h, err := s.CreateNode(ctx, node)
	require.NoError(t, err)
	var requests []driver.AttachmentRequest
	var specs []driver.IsolatedNetworkSpec
	var netIDs []substrate.OperationIdentity
	for i, name := range []string{"front", "back"} {
		nid := id
		nid.ResourceUID = uuid.NewString()
		spec := driver.IsolatedNetworkSpec{Name: "sbboot-" + nid.ResourceUID[:8], Bridge: "br0", CIDR: []string{"10.93.1.1/24", "10.93.2.1/24"}[i]}
		require.NoError(t, (network.Driver{}).CreateIsolated(substrate.WithOperationIdentity(ctx, nid), spec))
		raw, _ := json.Marshal(networkState{NetNS: spec.Name, Bridge: "br0"})
		req := driver.AttachmentRequest{Name: name, NetworkIdentity: nid, NetworkState: raw, MAC: []string{"02:00:00:00:03:01", "02:00:00:00:04:01"}[i], IPPrefixes: []string{[]string{"10.93.1.2/24", "10.93.2.2/24"}[i]}}
		requests = append(requests, req)
		_, err = s.Attach(ctx, h, req)
		require.NoError(t, err)
		specs = append(specs, spec)
		netIDs = append(netIDs, nid)
	}
	defer func() {
		if t.Failed() {
			raw, _ := os.ReadFile(filepath.Join(h.Provider.(*HandleState).VMDir, "firecracker.log"))
			t.Log(string(raw))
		}
	}()
	require.NoError(t, s.StartNode(ctx, h))
	m, err := s.loadManifest(ctx, h)
	require.NoError(t, err)
	require.NoError(t, verifyJailedProcess(m, m.Process))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "ip", Args: []string{"-4", "addr", "show"}, Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Stdout, "10.93.1.2/24")
	require.Contains(t, result.Stdout, "10.93.2.2/24")
	require.NoError(t, s.StopNode(ctx, h))
	require.NoError(t, s.StartNode(ctx, h))
	oldFabric := *h.Provider.(*HandleState).Fabric
	target := id
	target.Generation = 2
	target.ActionID = "reset"
	resetCtx := substrate.WithOperationIdentity(ctx, target)
	reset, err := s.PrepareReset(resetCtx, substrate.ResetRequest{Current: h, Node: node, Baseline: baseline})
	require.NoError(t, err)
	rawReset, err := s.MarshalResetHandle(reset)
	require.NoError(t, err)
	cold := realFirecracker(t, kernel, bin, s.rootfsDir)
	cold.jailerBin = s.jailerBin
	cold.cgroupParent = s.cgroupParent
	restored, err := cold.UnmarshalResetHandle(rawReset)
	require.NoError(t, err)
	restored.Request = reset.Request
	require.NoError(t, cold.DestroyReset(resetCtx, restored))
	require.NoError(t, network.VMNamespaceAbsent(ctx, oldFabric))
	replacement, err := cold.ApplyReset(resetCtx, restored)
	require.NoError(t, err)
	for _, req := range requests {
		_, err = cold.Attach(resetCtx, replacement, req)
		require.NoError(t, err)
	}
	require.NotEqual(t, oldFabric.Name, replacement.Provider.(*HandleState).Fabric.Name)
	require.NoError(t, cold.StartNode(resetCtx, replacement))
	conn, err = cold.Connection(replacement, nil)
	require.NoError(t, err)
	result, err = conn.Exec(resetCtx, substrate.ExecRequest{Program: "ip", Args: []string{"-4", "addr", "show"}, Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.Contains(t, result.Stdout, "10.93.1.2/24")
	require.Contains(t, result.Stdout, "10.93.2.2/24")
	require.NoError(t, cold.DestroyNode(resetCtx, replacement))
	require.NoError(t, network.VMNamespaceAbsent(ctx, *replacement.Provider.(*HandleState).Fabric))
	require.NoError(t, s.DestroyNode(ctx, h))
	require.NoError(t, network.VMNamespaceAbsent(ctx, *h.Provider.(*HandleState).Fabric))
	for i, spec := range specs {
		require.NoError(t, (network.Driver{}).DeleteIsolated(substrate.WithOperationIdentity(context.Background(), netIDs[i]), spec))
	}
}
