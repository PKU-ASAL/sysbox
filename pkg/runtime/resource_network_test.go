package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestNetworkResourceHandlerCreateAndDeleteIsolated(t *testing.T) {
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

func TestNetworkResourceHandlerPlanDiff(t *testing.T) {
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

func TestNetworkResourceHandlerRegistered(t *testing.T) {
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

// failingNetworkDriver is a docker Network driver whose RemoveManagedNetwork
// always fails, so a test can assert the runtime does not silently drop the
// state entry when network removal errors.
type failingNetworkDriver struct{}

func (failingNetworkDriver) CreateManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	return substrate.ManagedNetworkInfo{}, nil
}
func (failingNetworkDriver) RemoveManagedNetwork(context.Context, string) error {
	return fmt.Errorf("remove failed")
}
func (failingNetworkDriver) ReadManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	return substrate.ManagedNetworkInfo{}, nil
}
func (failingNetworkDriver) AllowEgress(context.Context, string) error  { return nil }
func (failingNetworkDriver) RemoveEgress(context.Context, string) error { return nil }

type recordingNetworkDriver struct{ spec substrate.ManagedNetworkSpec }

func (r *recordingNetworkDriver) CreateManagedNetwork(_ context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	r.spec = spec
	return substrate.ManagedNetworkInfo{ID: "nat-network", Name: "sysbox-nat-lab"}, nil
}
func (recordingNetworkDriver) RemoveManagedNetwork(context.Context, string) error { return nil }
func (recordingNetworkDriver) ReadManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	return substrate.ManagedNetworkInfo{}, nil
}
func (recordingNetworkDriver) AllowEgress(context.Context, string) error  { return nil }
func (recordingNetworkDriver) RemoveEgress(context.Context, string) error { return nil }

func TestNetworkResourceHandlerCreateNATUsesManagedNetwork(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	recorder := &recordingNetworkDriver{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Network: recorder}))
	defer func() { driver.DefaultRegistry = previous }()

	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	n := &graph.Node{Address: address.Resource("sysbox_network", "lab"), Data: &config.NetworkConfig{CIDR: "10.204.0.0/24", NAT: true}}
	res, err := (NetworkResourceHandler{}).Create(context.Background(), &ProviderContext{exec: exec}, n)
	require.NoError(t, err)
	require.Equal(t, "docker", res.Driver)
	require.True(t, res.IsNAT())
	require.Equal(t, "10.204.0.0/24", recorder.spec.CIDR)
	require.True(t, recorder.spec.NAT)
}

// A NAT network whose docker removal fails must keep its state entry, so a
// later destroy retries the removal instead of leaving an orphan network that
// makes the next apply of the same CIDR fail with "Pool overlaps".
func TestNetworkResourceHandlerDeleteKeepsStateOnRemoveFailure(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Network: failingNetworkDriver{}}))
	defer func() { driver.DefaultRegistry = previous }()

	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	addr := address.Resource("sysbox_network", "lab")
	res := state.Resource{
		Address:    addr,
		Driver:     "docker",
		Attributes: state.MustAttributes(map[string]any{"nat": true, "docker_network_id": "net-1", "cidr": "10.77.0.0/24"}),
	}
	exec.state.AddResource(res)
	// AddResource normalizes the resource (docker_network_id is a runtime-private
	// key, moved to Private.Runtime); read it back so Delete sees the same shape
	// the real apply path produces.
	stored := exec.state.FindResource(addr)
	require.NotNil(t, stored)

	p := NetworkResourceHandler{}
	err := p.Delete(context.Background(), &ProviderContext{exec: exec}, *stored)
	require.Error(t, err)
	require.NotNil(t, exec.state.FindResource(addr),
		"state entry must survive a failed network removal so destroy can retry")
}
