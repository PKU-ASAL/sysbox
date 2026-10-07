package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

type exposedBridgeTestDriver struct{ recordingNetworkDriver }

func (*exposedBridgeTestDriver) VerifyManagedBridge(context.Context, substrate.ManagedBridgeTarget) error {
	return nil
}
func (*exposedBridgeTestDriver) PreflightManagedBridge(context.Context) error { return nil }

func TestExposureUpgradeRejectsImplicitLiveAttachmentRewire(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Network: &exposedBridgeTestDriver{}}))
	g := graph.New()
	network := address.Resource("sysbox_network", "nat")
	node := address.Resource("sysbox_node", "vm")
	require.NoError(t, g.AddNode(network, nil))
	require.NoError(t, g.SetData(network, &config.NetworkConfig{NAT: true}))
	require.NoError(t, g.AddNode(node, []address.Address{network}))
	require.NoError(t, g.SetData(node, &config.NodeConfig{Substrate: "firecracker", Links: []config.LinkConfig{{Network: "sysbox_network.nat.id"}}}))
	executor := NewExecutor(g, &state.State{Resources: []state.Resource{{Address: network}, {Address: address.Resource("sysbox_node", "existing"), Attachments: []state.Attachment{{Network: network}}}}})
	require.ErrorContains(t, executor.preflightManagedBridges(context.Background()), "remove existing attached nodes")
}

func TestAddingMixedConsumerChangesNetworkDesiredIdentity(t *testing.T) {
	g := graph.New()
	network := address.Resource("sysbox_network", "nat")
	node := address.Resource("sysbox_node", "vm")
	require.NoError(t, g.AddNode(network, nil))
	cfg := &config.NetworkConfig{NAT: true, CIDR: "10.0.0.0/24"}
	require.NoError(t, g.SetData(network, cfg))
	attrs := map[string]any{}
	require.NoError(t, setDesiredHash(g.Get(network), attrs))
	current := state.Resource{Address: network, Attributes: state.MustAttributes(attrs)}
	require.NoError(t, g.AddNode(node, []address.Address{network}))
	require.NoError(t, g.SetData(node, &config.NodeConfig{Substrate: "firecracker", Links: []config.LinkConfig{{Network: "sysbox_network.nat.id"}}}))
	inferManagedBridgeExposure(g, &state.State{Resources: []state.Resource{current}})
	change, err := (NetworkResourceHandler{}).PlanDiff(g.Get(network), &current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, change.Action)
}

func TestManagedBridgeExposureFollowsConsumers(t *testing.T) {
	for _, provider := range []string{"docker", "firecracker", "libvirt"} {
		t.Run(provider, func(t *testing.T) {
			g := graph.New()
			node := address.Resource("sysbox_node", "web")
			network := address.Resource("sysbox_network", "nat")
			require.NoError(t, g.AddNode(node, nil))
			require.NoError(t, g.SetData(node, &config.NodeConfig{Substrate: provider, Links: []config.LinkConfig{{Name: "wan", Network: "sysbox_network.nat.id"}}}))
			require.Equal(t, provider != "docker", requiresManagedBridge(g, network))
			require.False(t, requiresManagedBridge(g, address.Resource("sysbox_network", "other")))
		})
	}
}
