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

func TestRouterResourceHandlerRegistered(t *testing.T) {
	p, ok := GetResourceHandler("sysbox_router")
	require.True(t, ok)
	require.Equal(t, "sysbox_router", p.Type())
	require.Equal(t, "sysbox_router", p.Schema().Type)
}

func TestRouterResourceHandlerPlanDiff(t *testing.T) {
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
	p := RouterResourceHandler{}

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

func TestRouterResourceHandlerDeleteMissingSubstrateReturnsError(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{
		Address:    address.Resource("sysbox_router", "r1"),
		Driver:     "missing-router-provider",
		Attributes: map[string]any{"container_id": "router"},
	}

	err := RouterResourceHandler{}.Delete(context.Background(), &ProviderContext{exec: exec}, res)
	require.Error(t, err)
}

func TestRouterNATUsesLogicalAttachments(t *testing.T) {
	sub := &portTestSubstrate{name: "router-test"}
	registerPortTestDriver(t, sub)
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_image", "router"), Attributes: map[string]any{"image_id": "image", "repository": "router:latest"}})
	st.AddResource(state.Resource{Address: address.Resource("sysbox_network", "internal"), Attributes: map[string]any{"netns": "ns", "bridge": "br"}})
	st.AddResource(state.Resource{Address: address.Resource("sysbox_network", "public"), Attributes: map[string]any{"nat": true, "docker_network_id": "net-1"}})
	exec := NewExecutor(graph.New(), st)
	n := &graph.Node{Address: address.Resource("sysbox_router", "edge"), Data: &config.RouterConfig{Image: "sysbox_image.router", Substrate: sub.name, NatFrom: "internal", NatTo: "uplink", Interfaces: []config.RouterInterface{{Name: "internal", Network: "sysbox_network.internal", IP: "10.0.0.1/24"}, {Name: "uplink", Network: "sysbox_network.public", IP: "172.20.0.2/24"}}}}
	res, err := RouterResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)
	require.NoError(t, err)
	require.Equal(t, []string{"internal", "uplink"}, sub.natNames)
	require.Len(t, res.Attachments, 2)
}
