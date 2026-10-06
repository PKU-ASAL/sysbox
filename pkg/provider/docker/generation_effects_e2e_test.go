//go:build e2e

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types/container"
	dockernet "github.com/docker/docker/api/types/network"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDockerRealGenerationEffectsE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	image := os.Getenv("SYSBOX_DOCKER_E2E_IMAGE")
	if image == "" {
		image = "alpine:3.22"
	}
	inspected, _, err := s.cli.ImageInspectWithRaw(ctx, image)
	require.NoError(t, err, "use a cached image; tests do not pull")
	identity := substrate.OperationIdentity{OwnerUID: fmt.Sprintf("e2e-%d", time.Now().UnixNano()), ResourceUID: "node", TopologyUID: "test", Generation: 2, ActionID: "create"}
	valid := substrate.WithOperationIdentity(ctx, identity)
	name := "sysbox-fence-" + identity.OwnerUID
	h, err := s.CreateNode(valid, substrate.NodeSpec{Name: name, Image: substrate.ArtifactHandle{ID: inspected.ID}, ManagedNetwork: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		require.NoError(t, s.cli.ContainerRemove(cleanup, h.ID, container.RemoveOptions{Force: true}))
	})
	require.NoError(t, s.StartNode(valid, h))
	old := identity
	old.Generation = 1
	stale := substrate.WithOperationIdentity(ctx, old)
	before, err := s.cli.ContainerInspect(ctx, h.ID)
	require.NoError(t, err)
	for operation, call := range map[string]func(context.Context, substrate.NodeHandle) error{"start": s.StartNode, "stop": s.StopNode, "pause": s.Pause, "resume": s.Resume, "destroy": s.DestroyNode} {
		t.Run(operation, func(t *testing.T) {
			require.Error(t, call(stale, h))
			after, err := s.cli.ContainerInspect(ctx, h.ID)
			require.NoError(t, err)
			require.True(t, after.State.Running)
			require.Equal(t, before.State.Pid, after.State.Pid)
		})
	}
	request := substrate.ExecRequest{Program: "sh", Args: []string{"-c", "echo stale > /tmp/sysbox-stale"}, Shell: substrate.ShellNone}
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	_, err = conn.Exec(stale, request, nil, nil)
	require.Error(t, err)
	_, err = conn.(substrate.ConnectionStarter).Start(stale, request)
	require.Error(t, err)
	file := filepath.Join(t.TempDir(), "payload")
	require.NoError(t, os.WriteFile(file, []byte("stale"), 0600))
	require.Error(t, s.CopyToNode(stale, h, file, "/tmp/sysbox-stale-copy", 0600))
	result, err := conn.Exec(valid, substrate.ExecRequest{Program: "sh", Args: []string{"-c", "test ! -e /tmp/sysbox-stale && test ! -e /tmp/sysbox-stale-copy"}, Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Zero(t, result.ExitCode)
	// A new provider can discover this exact generation and safely address its ID.
	cold, err := New()
	require.NoError(t, err)
	defer cold.Close()
	discovered, err := cold.DiscoverManagedNode(valid, identity, name)
	require.NoError(t, err)
	require.Equal(t, h.ID, discovered.ID)
	require.NoError(t, cold.Pause(valid, discovered))
	require.NoError(t, cold.Resume(valid, discovered))
	t.Run("policy-target", func(t *testing.T) {
		pid, err := s.policyTargetPID(ctx, dockerPolicyTarget{ContainerID: h.ID, Identity: identity})
		require.NoError(t, err)
		require.Equal(t, before.State.Pid, pid)
		raw, err := json.Marshal(dockerPolicyTarget{ContainerID: h.ID, Identity: old})
		require.NoError(t, err)
		target := driver.PolicyTarget{Resource: "sysbox_node.test", Identity: old, Policy: identity, State: raw}
		_, err = s.ApplyRuleset(ctx, target, driver.RulesetSpec{Owner: "test-policy"})
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		_, err = s.ObserveRuleset(ctx, target, "test-policy")
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		err = s.DeleteRuleset(ctx, target, "test-policy")
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
	})
	t.Run("attachment", func(t *testing.T) {
		// Docker permits a requested static address only on explicitly
		// configured IPAM subnets. A collision fails creation without adoption.
		seed := time.Now().UnixNano()
		cidr := fmt.Sprintf("10.%d.%d.0/24", 128+(seed>>8)%100, seed%256)
		created, err := s.cli.NetworkCreate(ctx, name+"-net", dockernet.CreateOptions{Driver: "bridge", Internal: true, IPAM: &dockernet.IPAM{Config: []dockernet.IPAMConfig{{Subnet: cidr}}}})
		require.NoError(t, err)
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = s.cli.NetworkDisconnect(cleanup, created.ID, h.ID, true)
			require.NoError(t, s.cli.NetworkRemove(cleanup, created.ID))
		}()
		info, err := s.cli.NetworkInspect(ctx, created.ID, dockernet.InspectOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, info.IPAM.Config)
		ip, subnet, err := net.ParseCIDR(info.IPAM.Config[0].Subnet)
		require.NoError(t, err)
		ip = ip.To4()
		require.NotNil(t, ip)
		ip[3] += 2
		ones, _ := subnet.Mask.Size()
		raw, err := json.Marshal(networkState{NAT: true, NetworkID: created.ID})
		require.NoError(t, err)
		req := driver.AttachmentRequest{Name: "test", NetworkState: raw, IPPrefixes: []string{fmt.Sprintf("%s/%d", ip, ones)}}
		_, err = s.Attach(stale, h, req)
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		attachment, err := s.Attach(valid, h, req)
		require.NoError(t, err)
		err = s.Delete(stale, h, req, attachment.State)
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		_, err = s.Observe(valid, h, req, attachment.State)
		require.NoError(t, err)
		require.NoError(t, s.Delete(valid, h, req, attachment.State))
		_, err = s.Observe(valid, h, req, attachment.State)
		require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
	})
	t.Logf("real container %s: stale lifecycle/exec/copy rejected; matching generation exec and rediscovery passed", h.ID)
}
