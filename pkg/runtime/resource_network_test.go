package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	netprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
)

func TestNetworkResourceProviderCreateAndDeleteIsolated(t *testing.T) {
	restore := stubNetworkOps(t)
	defer restore()
	n := &graph.Node{
		ID: graph.NodeID{Type: "sysbox_network", Name: "dmz"},
		Data: &config.NetworkConfig{
			CIDR: "10.10.0.0/24",
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	p := NetworkResourceProvider{}

	res, err := p.Create(context.Background(), &ProviderContext{exec: exec}, n)
	require.NoError(t, err)
	require.Equal(t, "sysbox_network", res.Type)
	require.Equal(t, "dmz", res.Name)
	require.Equal(t, "network", res.Provider)
	require.Equal(t, "sysbox-net-dmz", res.NetNS())
	require.Equal(t, "br-dmz", res.Bridge())
	require.Equal(t, "10.10.0.1/24", res.Str("gateway"))
	require.NotEmpty(t, res.Str(desiredHashKey))

	exec.state.AddResource(res)
	require.NoError(t, p.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, exec.state.FindResource("sysbox_network", "dmz"))
}

func TestNetworkResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		ID:   graph.NodeID{Type: "sysbox_network", Name: "dmz"},
		Data: &config.NetworkConfig{CIDR: "10.10.0.0/24"},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Type: "sysbox_network", Name: "dmz", Provider: "network", Instance: inst}
	p := NetworkResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.NetworkConfig{CIDR: "10.20.0.0/24"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	require.Contains(t, action.Changes, "cidr")
}

func TestNetworkResourceProviderRegistered(t *testing.T) {
	p, ok := GetResourceProvider("sysbox_network")
	require.True(t, ok)
	require.Equal(t, "sysbox_network", p.Type())
}

func stubNetworkOps(t *testing.T) func() {
	t.Helper()
	oldCreateNetns := createNetnsFn
	oldDeleteNetns := deleteNetnsFn
	oldCreateBridge := createBridgeFn
	oldDeleteBridge := deleteBridgeFn
	createNetnsFn = func(string) error { return nil }
	deleteNetnsFn = func(string) error { return nil }
	createBridgeFn = func(netprovider.BridgeConfig) error { return nil }
	deleteBridgeFn = func(netprovider.BridgeConfig) error { return nil }
	return func() {
		createNetnsFn = oldCreateNetns
		deleteNetnsFn = oldDeleteNetns
		createBridgeFn = oldCreateBridge
		deleteBridgeFn = oldDeleteBridge
	}
}
