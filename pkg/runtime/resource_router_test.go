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

func TestRouterResourceProviderRegistered(t *testing.T) {
	p, ok := GetResourceProvider("sysbox_router")
	require.True(t, ok)
	require.Equal(t, "sysbox_router", p.Type())
	require.Equal(t, "sysbox_router", p.Schema().Type)
}

func TestRouterResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_router", "r1"),
		Data: &config.RouterConfig{
			Image:     "sysbox_image.alpine.id",
			Substrate: "docker",
			Interfaces: []config.RouterInterface{{
				Name:    "lan",
				Network: "sysbox_network.lan.id",
				IP:      "10.0.1.1/24",
			}},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_router", "r1"), Driver: "docker", Attributes: inst}
	p := RouterResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.RouterConfig{
		Image:     "sysbox_image.alpine.id",
		Substrate: "docker",
		Interfaces: []config.RouterInterface{{
			Name:    "lan",
			Network: "sysbox_network.lan.id",
			IP:      "10.0.2.1/24",
		}},
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	_, ok := fieldChangeAt(action.Changes, "interfaces[0].IP")
	require.True(t, ok)
}

func TestRouterResourceProviderDeleteMissingSubstrateReturnsError(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{
		Address:    address.Resource("sysbox_router", "r1"),
		Driver:     "missing-router-provider",
		Attributes: map[string]any{"container_id": "router"},
	}

	err := RouterResourceProvider{}.Delete(context.Background(), &ProviderContext{exec: exec}, res)
	require.Error(t, err)
}
