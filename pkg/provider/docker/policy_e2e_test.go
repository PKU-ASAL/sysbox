//go:build e2e

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestDockerStartPreservesNamedNetworkNamespaceE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	namespace := fmt.Sprintf("sysbox-e2e-docker-ns-%d", os.Getpid())
	bridge := "sbe2ebr"
	networkID := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: namespace, Generation: 1}
	networkCtx := substrate.WithOperationIdentity(ctx, networkID)
	networkSpec := driver.IsolatedNetworkSpec{Name: namespace, Bridge: bridge, CIDR: "10.250.0.1/24"}
	require.NoError(t, (networkprovider.Driver{}).CreateIsolated(networkCtx, networkSpec))
	t.Cleanup(func() {
		require.NoError(t, (networkprovider.Driver{}).DeleteIsolated(substrate.WithOperationIdentity(context.Background(), networkID), networkSpec))
	})
	provider, err := New()
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	defer provider.Close()
	name := fmt.Sprintf("sysbox-e2e-netns-%d", os.Getpid())
	created, err := provider.cli.ContainerCreate(ctx, &containertypes.Config{Image: "alpine:latest", Cmd: []string{"sleep", "30"}}, &containertypes.HostConfig{NetworkMode: "none", CapAdd: []string{"NET_ADMIN"}, Sysctls: map[string]string{"net.ipv4.ip_forward": "1"}}, nil, nil, name)
	if err != nil {
		t.Skipf("Docker netns E2E requires local alpine:latest: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.cli.ContainerRemove(context.Background(), created.ID, containertypes.RemoveOptions{Force: true})
	})
	require.NoError(t, provider.cli.ContainerStart(ctx, created.ID, containertypes.StartOptions{}))
	require.True(t, (networkprovider.Driver{}).LinkHealthy(ctx, namespace, "lo"))
	pair, err := networkprovider.CreateVethPair(ctx, networkprovider.BridgeTarget{Identity: networkID, NetworkNamespace: namespace, Bridge: bridge}, networkprovider.VethSpec{OwnerAlias: "test-owned", HostEnd: "sbe2evh", GuestEnd: "sbe2evg"})
	require.NoError(t, err)
	proof, err := networkprovider.CaptureAttachmentLink(ctx, pair.NetnsName, pair.HostEnd, "test-owned")
	require.NoError(t, err)
	require.NoError(t, networkprovider.DeleteOwnedAttachment(ctx, proof, "test-owned"))
}

func TestDockerOwnedPolicyLifecycleE2E(t *testing.T) {
	identity := substrate.OperationIdentity{OwnerUID: "policy-e2e", ResourceUID: fmt.Sprintf("node-%d", os.Getpid()), TopologyUID: "policy-e2e", Generation: 1, ActionID: "create"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	provider, err := New()
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	defer provider.Close()
	name := fmt.Sprintf("sysbox-e2e-policy-%d", os.Getpid())
	created, err := provider.cli.ContainerCreate(ctx, &containertypes.Config{Image: "alpine:latest", Cmd: []string{"sleep", "30"}, Labels: operationLabels(identity)}, &containertypes.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Skipf("Docker policy E2E requires local alpine:latest: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.cli.ContainerRemove(context.Background(), created.ID, containertypes.RemoveOptions{Force: true})
	})
	require.NoError(t, provider.cli.ContainerStart(ctx, created.ID, containertypes.StartOptions{}))
	targetRaw, err := json.Marshal(dockerPolicyTarget{ContainerID: created.ID, Identity: identity, Bindings: map[string]string{"inside": "lo", "uplink": "lo"}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_router.e2e", Identity: identity, Policy: identity, State: targetRaw}
	spec := driver.RulesetSpec{Owner: "e2e/sysbox_router.docker", Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop,
		NAT: &driver.NATPolicy{SourceAttachment: "inside", UplinkAttachment: "uplink", SourceCIDRs: []string{"127.0.0.0/8"}, Masquerade: true}}

	first, err := provider.ApplyRuleset(ctx, target, spec)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Skipf("Docker firewall E2E requires access to the container network namespace: %v", err)
	}
	require.NoError(t, err)
	second, err := provider.ApplyRuleset(ctx, target, spec)
	require.NoError(t, err)
	require.Equal(t, first.Table, second.Table)
	require.Equal(t, first.Digest, second.Digest)
	observed, err := provider.ObserveRuleset(ctx, target, spec.Owner)
	require.NoError(t, err)
	require.Equal(t, first.Digest, observed.Digest)
	require.NoError(t, provider.DeleteRuleset(ctx, target, spec.Owner))
	_, err = provider.ObserveRuleset(ctx, target, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), err)
}

func TestDockerOwnedPolicyStoppedContainerIsDriftE2E(t *testing.T) {
	identity := substrate.OperationIdentity{OwnerUID: "policy-e2e", ResourceUID: fmt.Sprintf("node-%d", os.Getpid()), TopologyUID: "policy-e2e", Generation: 1, ActionID: "create"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	provider, err := New()
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	defer provider.Close()
	name := fmt.Sprintf("sysbox-e2e-policy-stopped-%d", os.Getpid())
	created, err := provider.cli.ContainerCreate(ctx, &containertypes.Config{Image: "alpine:latest", Cmd: []string{"sleep", "30"}, Labels: operationLabels(identity)}, &containertypes.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Skipf("Docker policy E2E requires local alpine:latest: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.cli.ContainerRemove(context.Background(), created.ID, containertypes.RemoveOptions{Force: true})
	})
	require.NoError(t, provider.cli.ContainerStart(ctx, created.ID, containertypes.StartOptions{}))
	targetRaw, err := json.Marshal(dockerPolicyTarget{ContainerID: created.ID, Identity: identity, Bindings: map[string]string{"inside": "lo", "uplink": "lo"}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_router.e2e", Identity: identity, Policy: identity, State: targetRaw}
	spec := driver.RulesetSpec{Owner: "e2e/sysbox_router.stopped", Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop}
	_, err = provider.ApplyRuleset(ctx, target, spec)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Skipf("Docker firewall E2E requires access to the container network namespace: %v", err)
	}
	require.NoError(t, err)
	require.NoError(t, provider.cli.ContainerStop(ctx, created.ID, containertypes.StopOptions{}))
	_, err = provider.ObserveRuleset(ctx, target, spec.Owner)
	require.Error(t, err)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), err)
}
