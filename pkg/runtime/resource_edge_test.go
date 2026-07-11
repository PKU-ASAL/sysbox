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
		Address: address.Address{Type: "sysbox_firewall", Name: "allow_ssh"},
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
	current := &state.Resource{Type: "sysbox_firewall", Name: "allow_ssh", Provider: "network", Instance: inst}
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
	require.Contains(t, action.Changes, "rules")
}

func TestSSHAccessResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Address{Type: "sysbox_ssh_access", Name: "admin"},
		Data: &config.SSHAccessConfig{
			Node:           "sysbox_node.web.id",
			AuthorizedKeys: []string{"ssh-ed25519 old"},
			Port:           22,
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Type: "sysbox_ssh_access", Name: "admin", Provider: "docker", Instance: inst}
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
	require.Contains(t, action.Changes, "authorized_keys")
	require.True(t, action.Changes["authorized_keys"].Sensitive)
}

func TestActorResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Address{Type: "sysbox_actor", Name: "agent"},
		Data: &config.ActorConfig{
			Position: "internal",
			Node:     "sysbox_node.web.id",
			Command:  []string{"sleep", "60"},
			Env:      map[string]string{"TOKEN": "old"},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Type: "sysbox_actor", Name: "agent", Provider: "docker", Instance: inst}
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
	require.Contains(t, action.Changes, "env")
	require.True(t, action.Changes["env"].Sensitive)
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
			res:  state.Resource{Type: "sysbox_ssh_access", Name: "admin"},
		},
		{
			name: "actor_missing_substrate",
			p:    ActorResourceProvider{},
			res:  state.Resource{Type: "sysbox_actor", Name: "agent", Provider: "missing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{tc.res}}
			exec := NewExecutor(graph.New(), st)
			require.NoError(t, tc.p.Delete(context.Background(), &ProviderContext{exec: exec}, tc.res))
			require.Nil(t, st.FindResource(tc.res.Type, tc.res.Name))
		})
	}
}
