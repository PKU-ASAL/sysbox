package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestEdgeResourceProvidersRegistered(t *testing.T) {
	for _, typ := range []string{"sysbox_firewall", "sysbox_ssh_access", "sysbox_actor"} {
		p, ok := GetResourceProvider(typ)
		require.True(t, ok, typ)
		require.Equal(t, typ, p.Type())
		require.Equal(t, typ, p.Schema().Type)
	}
}

func TestFirewallResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_firewall", "allow_ssh"),
		Data: &config.FirewallConfig{
			AttachTo: "sysbox_network.dmz.id",
			Rules: []config.FirewallRule{{
				Proto:  "tcp",
				DPort:  22,
				SrcNet: "10.0.0.0/24",
				Action: "accept",
			}},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_firewall", "allow_ssh"), Driver: "network", Attributes: inst}
	p := FirewallResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.FirewallConfig{
		AttachTo: "sysbox_network.dmz.id",
		Rules: []config.FirewallRule{{
			Proto:  "tcp",
			DPort:  443,
			SrcNet: "10.0.0.0/24",
			Action: "accept",
		}},
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	_, ok := fieldChangeAt(action.Changes, "rules[0].DPort")
	require.True(t, ok)
}

func TestSSHAccessResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_ssh_access", "admin"),
		Data: &config.SSHAccessConfig{
			Node:           "sysbox_node.web.id",
			AuthorizedKeys: []string{"ssh-ed25519 old"},
			Port:           22,
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_ssh_access", "admin"), Driver: "docker", Attributes: inst}
	p := SSHAccessResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.SSHAccessConfig{
		Node:           "sysbox_node.web.id",
		AuthorizedKeys: []string{"ssh-ed25519 new"},
		Port:           22,
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	change, ok := fieldChangeAt(action.Changes, "authorized_keys[0]")
	require.True(t, ok)
	require.True(t, change.Sensitive)
}

func TestActorResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_actor", "agent"),
		Data: &config.ActorConfig{
			Position: "internal",
			Node:     "sysbox_node.web.id",
			Command:  []string{"sleep", "60"},
			Env:      map[string]string{"TOKEN": "old"},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_actor", "agent"), Driver: "docker", Attributes: inst}
	p := ActorResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.ActorConfig{
		Position: "internal",
		Node:     "sysbox_node.web.id",
		Command:  []string{"sleep", "60"},
		Env:      map[string]string{"TOKEN": "new"},
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	change, ok := fieldChangeAt(action.Changes, "env.TOKEN")
	require.True(t, ok)
	require.True(t, change.Sensitive)
}

func TestEdgeProviderDeleteRemovesState(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ResourceProvider
		res  state.Resource
	}{
		{
			name: "ssh_access",
			p:    SSHAccessResourceProvider{},
			res:  state.Resource{Address: address.Resource("sysbox_ssh_access", "admin")},
		},
		{
			name: "actor_missing_substrate",
			p:    ActorResourceProvider{},
			res:  state.Resource{Address: address.Resource("sysbox_actor", "agent"), Driver: "missing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{tc.res}}
			exec := NewExecutor(graph.New(), st)
			require.NoError(t, tc.p.Delete(context.Background(), &ProviderContext{exec: exec}, tc.res))
			require.Nil(t, st.FindResource(tc.res.Address))
		})
	}
}
