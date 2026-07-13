//go:build e2e

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
)

func TestDockerStartPreservesNamedNetworkNamespaceE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	namespace := fmt.Sprintf("sysbox-e2e-docker-ns-%d", os.Getpid())
	require.NoError(t, networkprovider.CreateNetns(namespace))
	t.Cleanup(func() { _ = networkprovider.DeleteNetns(namespace) })
	bridge := "sbe2ebr"
	require.NoError(t, networkprovider.CreateBridge(networkprovider.BridgeConfig{NetnsName: namespace, BridgeName: bridge, CIDR: "10.250.0.1/24"}))
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
	pair, err := networkprovider.CreateVethPair(networkprovider.VethSpec{HostEnd: "sbe2evh", GuestEnd: "sbe2evg", NetnsName: namespace, BridgeName: bridge})
	require.NoError(t, err)
	require.NoError(t, networkprovider.DeleteVethPair(pair))
}

func TestDockerOwnedPolicyLifecycleE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	provider, err := New()
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	defer provider.Close()
	name := fmt.Sprintf("sysbox-e2e-policy-%d", os.Getpid())
	created, err := provider.cli.ContainerCreate(ctx, &containertypes.Config{Image: "alpine:latest", Cmd: []string{"sleep", "30"}}, &containertypes.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Skipf("Docker policy E2E requires local alpine:latest: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.cli.ContainerRemove(context.Background(), created.ID, containertypes.RemoveOptions{Force: true})
	})
	require.NoError(t, provider.cli.ContainerStart(ctx, created.ID, containertypes.StartOptions{}))
	targetRaw, err := json.Marshal(dockerPolicyTarget{ContainerID: created.ID, Bindings: map[string]string{"inside": "lo", "uplink": "lo"}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_router.e2e", State: targetRaw}
	spec := driver.RulesetSpec{Owner: "e2e/sysbox_router.docker", Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop,
		NAT: &driver.NATPolicy{SourceAttachment: "inside", UplinkAttachment: "uplink", SourceCIDRs: []string{"127.0.0.0/8"}, Masquerade: true}}

	first, err := provider.ApplyRuleset(ctx, target, spec)
	require.NoError(t, err)
	second, err := provider.ApplyRuleset(ctx, target, spec)
	require.NoError(t, err)
	require.Equal(t, first.Table, second.Table)
	require.Equal(t, first.Digest, second.Digest)
	require.Contains(t, second.Inventory, driver.OwnedObject{Kind: "chain", Name: "postrouting"})
	require.NoError(t, provider.DeleteRuleset(ctx, target, spec.Owner))
	_, err = provider.ObserveRuleset(ctx, target, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), err)
}
