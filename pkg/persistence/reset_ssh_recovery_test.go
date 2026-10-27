package persistence

import (
	"context"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type resetSSHRecoveryDriver struct {
	*continuationDriver
	connection *resetSSHRecoveryConnection
}

func (d *resetSSHRecoveryDriver) Connection(h substrate.NodeHandle, _ []substrate.ConnectionHint) (substrate.Connection, error) {
	d.connection.node = h.ID
	return d.connection, nil
}

type resetSSHRecoveryConnection struct {
	substrate.Connection
	substrate.ConnectionSSHAccess
	node   string
	result *substrate.SSHAccessResult
	config substrate.SSHAccessConfig
	calls  int
}

func (c *resetSSHRecoveryConnection) DiscoverSSHAccess(_ context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	r := substrate.SSHAccessDiscovery{Query: q, BootID: "guest", Status: "absent"}
	if c.result != nil {
		r.Status, r.Current, r.Config = "present", c.result, &c.config
	}
	return r, nil
}
func (c *resetSSHRecoveryConnection) MutateSSHAccess(ctx context.Context, request substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	c.calls++
	id, _ := substrate.OperationIdentityFromContext(ctx)
	digest, err := request.Digest()
	if err != nil {
		return substrate.SSHAccessResult{}, err
	}
	c.config = request.Config
	c.result = &substrate.SSHAccessResult{Present: true, Handle: substrate.SSHAccessHandle{ID: request.ID, Digest: digest, NodeID: c.node, User: request.Config.User, AccessID: request.Config.AccessID, Revision: request.ExpectedRevision + 1, BootID: "guest", Epoch: 1, TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: id.Generation}}
	return *c.result, errors.New("guest committed; reply lost")
}
func (c *resetSSHRecoveryConnection) InspectSSHAccess(_ context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	if c.result == nil || c.result.Handle != h {
		return substrate.SSHAccessInspection{}, errors.New("unknown child")
	}
	return substrate.SSHAccessInspection{Status: "present", Revision: h.Revision}, nil
}

func TestResetSSHRealRepositoryColdRecoveryDoesNotRepeatEffects(t *testing.T) {
	for _, backend := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			r := resetTestRepository(t, backend)
			ctx := context.Background()
			mgr := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := mgr.Load()
			require.NoError(t, err)
			conn := &resetSSHRecoveryConnection{}
			d := &resetSSHRecoveryDriver{continuationDriver: &continuationDriver{deleted: map[string]bool{}, created: map[string]bool{}}, connection: conn}
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "reset-ssh-test", Version: "1", Node: d, NodeState: d, NIC: d, Reset: d}))
			image, node, access := address.Resource("sysbox_image", "base"), address.Resource("sysbox_node", "web"), address.Resource("sysbox_ssh_access", "admin")
			g := graph.New()
			require.NoError(t, g.AddNode(image, nil))
			require.NoError(t, g.SetData(image, &config.ImageConfig{Substrate: "reset-ssh-test", Kind: "rootfs", Source: "/base", Architecture: "amd64", GuestFamily: "linux"}))
			require.NoError(t, g.AddNode(node, []address.Address{image}))
			nodeConfig := &config.NodeConfig{Substrate: "reset-ssh-test", Image: "sysbox_image.base.id", Connections: []config.ConnectionConfig{{User: "ubuntu"}}}
			require.NoError(t, g.SetData(node, nodeConfig))
			require.NoError(t, g.AddNode(access, []address.Address{node}))
			keys := []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBVJklJOs11mczPrSlMNBIqfguwuvQcCjdOgRqihdz5L"}
			require.NoError(t, g.SetData(access, &config.SSHAccessConfig{Node: node.String(), AuthorizedKeys: keys}))
			st.AddResource(state.Resource{Address: image, Driver: "reset-ssh-test", OwnerUID: st.TopologyUID, ResourceUID: "image", Generation: 1, Attributes: state.MustAttributes(map[string]any{"image_id": "base", "kind": "rootfs", "source": "/base", "sha256": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "architecture": "amd64", "guest_family": "linux"})})
			st.AddResource(state.Resource{Address: node, Driver: "reset-ssh-test", ExternalID: "old-web", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, Attributes: state.MustAttributes(map[string]any{"guest_family": "linux", "desired": map[string]any{"connections": []config.ConnectionConfig{{User: "ubuntu"}}}})})
			mutation := substrate.SSHAccessMutation{ID: "old-ssh", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: keys}}
			digest, err := mutation.Digest()
			require.NoError(t, err)
			handle := substrate.SSHAccessHandle{ID: mutation.ID, Digest: digest, NodeID: "old-web", User: "ubuntu", AccessID: "admin", Revision: 1, BootID: "old", Epoch: 1, TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
			st.AddResource(state.Resource{Address: access, Driver: "managed", ExternalID: "admin", SchemaVersion: 2, OwnerUID: st.TopologyUID, ResourceUID: "ssh", Generation: 1, Dependencies: []address.Address{node}, Attributes: state.MustAttributes(map[string]any{"ssh_access": map[string]any{"version": 1, "node": node.String(), "config": mutation.Config, "handle": handle}})})
			require.NoError(t, mgr.Save(st))
			st, err = mgr.Load()
			require.NoError(t, err)
			plan, err := runtime.BuildResetPlan(g, st, node.String())
			require.NoError(t, err)
			executor := runtime.NewExecutor(g, st)
			executor.SetRunContext("lab", "original")
			executor.SetActionRepository(r)
			require.ErrorContains(t, executor.Reset(ctx, plan), "reply lost")
			require.Equal(t, 1, d.destroyCalls)
			require.Equal(t, 1, d.applyCalls)
			require.Equal(t, 1, conn.calls)
			before, err := mgr.Load()
			require.NoError(t, err)
			require.EqualValues(t, 1, before.FindResource(node).Generation)
			require.EqualValues(t, 1, before.FindResource(access).Generation)
			cold := runtime.NewExecutor(g, before)
			cold.SetRunContext("lab", "recovery")
			cold.SetActionRepository(r)
			require.NoError(t, cold.RecoverRunActions(ctx, "original"))
			after, err := mgr.Load()
			require.NoError(t, err)
			require.EqualValues(t, 2, after.FindResource(node).Generation)
			require.EqualValues(t, 2, after.FindResource(access).Generation)
			require.Equal(t, 1, d.destroyCalls)
			require.Equal(t, 1, d.applyCalls)
			require.Equal(t, 1, conn.calls)
		})
	}
}
