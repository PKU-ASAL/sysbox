package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/stretchr/testify/require"
)

func TestExpandedNetworkNamesAreDistinct(t *testing.T) {
	path := writeHCL(t, `resource "sysbox_network" "lan" {
 for_each = { one = "10.1.0.0/24", two = "10.2.0.0/24" }
 cidr = each.value
 }`)
	root, err := config.ParseFile(path)
	require.NoError(t, err)
	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx, driver.DefaultRegistry)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, node := range g.All() {
		name := networkExternalName("review", node.Address)
		require.False(t, names[name], "expanded network name collision: %s", name)
		names[name] = true
	}
}

// Keep the ordinary naming contract stable; persisted external identities are
// authoritative and must not be renamed by a formatting change.
func TestPlainResourceNamesRemainStable(t *testing.T) {
	require.Equal(t, "sysbox-lab-lab-node-web", runtimeExternalName("lab", "node", address.Resource("sysbox_node", "web")))
	require.Equal(t, "lab-lab-net-lan", networkExternalName("lab", address.Resource("sysbox_network", "lan")))
}

func TestStructuredResourceNamesUseCanonicalAddress(t *testing.T) {
	names := map[string]bool{}
	for _, addr := range []address.Address{
		address.Resource("sysbox_node", "web"),
		address.IntInstance("sysbox_node", "web", 0), address.IntInstance("sysbox_node", "web", 1),
		address.StringInstance("sysbox_node", "web", "red/blue"), address.StringInstance("sysbox_node", "web", "red-blue"),
		address.Resource("sysbox_node", "web").WithModule(address.ModuleInstance{Name: "one"}),
		address.Resource("sysbox_node", "web").WithModule(address.ModuleInstance{Name: "two"}),
	} {
		name := runtimeExternalName("lab", "node", addr)
		require.False(t, names[name], "collision for %s", addr)
		require.Equal(t, name, runtimeExternalName("lab", "node", addr.Clone()))
		names[name] = true
	}
}

type addressNetworkDriver struct {
	recordingNetworkDriver
	created, discovered []string
}

func (d *addressNetworkDriver) CreateManagedNetwork(_ context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	d.created = append(d.created, spec.Name)
	return substrate.ManagedNetworkInfo{ID: spec.Name, Name: spec.Name}, nil
}
func (d *addressNetworkDriver) ReadManagedNetwork(_ context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	d.discovered = append(d.discovered, spec.Name)
	return substrate.ManagedNetworkInfo{}, driver.Wrap(driver.ErrorNotFound, "test", "absent", nil)
}
func TestNetworkCreationAndDiscoveryUseSameFullAddress(t *testing.T) {
	providers := driver.NewRegistry()
	d := &addressNetworkDriver{}
	require.NoError(t, providers.Register(driver.Descriptor{Name: "docker", Version: "1", Network: d}))
	e := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}, providers)
	e.SetRunContext("lab", "run")
	pc := &ProviderContext{exec: e}
	for _, addr := range []address.Address{address.StringInstance("sysbox_network", "lan", "one"), address.StringInstance("sysbox_network", "lan", "two"), address.Resource("sysbox_network", "lan").WithModule(address.ModuleInstance{Name: "inner"})} {
		n := &graph.Node{Address: addr, Data: &config.NetworkConfig{CIDR: "10.1.0.0/24", NAT: true}}
		observed, err := (NetworkResourceHandler{}).Discover(context.Background(), pc, n)
		require.NoError(t, err)
		require.Equal(t, state.ResourceAbsent, observed.Status)
		_, err = (NetworkResourceHandler{}).Create(context.Background(), pc, n)
		require.NoError(t, err)
	}
	require.Equal(t, d.created, d.discovered)
	require.Len(t, map[string]bool{d.created[0]: true, d.created[1]: true, d.created[2]: true}, 3)
}
