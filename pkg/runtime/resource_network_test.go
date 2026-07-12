package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestNetworkResourceProviderCreateAndDeleteIsolated(t *testing.T) {
	restore := stubNetworkOps(t)
	defer restore()
	n := &graph.Node{
		Address: address.Resource("sysbox_network", "dmz"),
		Data: &config.NetworkConfig{
			CIDR: "10.10.0.0/24",
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	p := NetworkResourceHandler{}

	res, err := p.Create(context.Background(), &ProviderContext{exec: exec}, n)
	require.NoError(t, err)
	require.Equal(t, "sysbox_network", res.Address.Type)
	require.Equal(t, "dmz", res.Address.Name)
	require.Equal(t, "network", res.Driver)
	require.Equal(t, "sysbox-net-dmz", res.NetNS())
	require.Equal(t, "br-dmz", res.Bridge())
	require.Equal(t, "10.10.0.1/24", res.Str("gateway"))
	require.NotEmpty(t, res.Str(desiredHashKey))

	exec.state.AddResource(res)
	require.NoError(t, p.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, exec.state.FindResource(address.Resource("sysbox_network", "dmz")))
}

func TestNetworkResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_network", "dmz"),
		Data:    &config.NetworkConfig{CIDR: "10.10.0.0/24"},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_network", "dmz"), Driver: "network", Attributes: inst}
	p := NetworkResourceHandler{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.NetworkConfig{CIDR: "10.20.0.0/24"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	_, ok := fieldChangeAt(action.Changes, "cidr")
	require.True(t, ok)
}

func TestNetworkResourceProviderRegistered(t *testing.T) {
	p, ok := GetResourceHandler("sysbox_network")
	require.True(t, ok)
	require.Equal(t, "sysbox_network", p.Type())
}

func stubNetworkOps(t *testing.T) func() {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "network", Version: "test", LinuxNetwork: fakeLinuxNetwork{}}))
	return func() { driver.DefaultRegistry = previous }
}

type fakeLinuxNetwork struct{}

func (fakeLinuxNetwork) CreateIsolated(context.Context, driver.IsolatedNetworkSpec) error { return nil }
func (fakeLinuxNetwork) DeleteIsolated(context.Context, driver.IsolatedNetworkSpec) error { return nil }
func (fakeLinuxNetwork) NetworkHealthy(context.Context, driver.IsolatedNetworkSpec) (bool, string) {
	return true, ""
}
func (fakeLinuxNetwork) LinkHealthy(context.Context, string, string) bool               { return true }
func (fakeLinuxNetwork) DeleteAttachment(context.Context, string, string, string) error { return nil }
func (fakeLinuxNetwork) ApplyFirewall(context.Context, string, []driver.FirewallRule) error {
	return nil
}
func (fakeLinuxNetwork) DeleteFirewall(context.Context, string) error { return nil }
