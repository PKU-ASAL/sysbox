package graph

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
)

func TestBuildAndTopoWalk(t *testing.T) {
	g := New()
	network := address.Resource("network", "dmz")
	image := address.Resource("image", "alpine")
	web := address.Resource("node", "web")
	require.NoError(t, g.AddNode(network, nil))
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.AddNode(web, []address.Address{network, image}))

	order, err := g.TopoSort()
	require.NoError(t, err)
	require.Greater(t, indexOf(order, web), indexOf(order, network))
	require.Greater(t, indexOf(order, web), indexOf(order, image))
}

func TestGraphKeepsModuleAndForEachInstancesDistinct(t *testing.T) {
	g := New()
	root := address.StringInstance("sysbox_node", "web", "blue")
	child := address.StringInstance("sysbox_node", "web", "blue").WithModule(address.ModuleInstance{Name: "lab"})
	require.NoError(t, g.AddNode(root, nil))
	require.NoError(t, g.AddNode(child, nil))

	require.Len(t, g.All(), 2)
	require.NotNil(t, g.Get(root))
	require.NotNil(t, g.Get(child))
}

func TestAddNodeRejectsDuplicateAddress(t *testing.T) {
	g := New()
	addr := address.Resource("node", "web")
	require.NoError(t, g.AddNode(addr, nil))
	require.ErrorContains(t, g.AddNode(addr, nil), "duplicate resource node.web")
}

func TestAddNodeOwnsAddressAndDependencies(t *testing.T) {
	g := New()
	addr := address.Resource("node", "web").WithModule(address.ModuleInstance{Name: "lab"})
	dep := address.Resource("network", "dmz").WithModule(address.ModuleInstance{Name: "lab"})
	require.NoError(t, g.AddNode(dep, nil))
	require.NoError(t, g.AddNode(addr, []address.Address{dep}))

	addr.ModulePath[0].Name = "mutated"
	dep.ModulePath[0].Name = "mutated"

	stored := g.Get(address.Resource("node", "web").WithModule(address.ModuleInstance{Name: "lab"}))
	require.NotNil(t, stored)
	require.Equal(t, `module.lab.node.web`, stored.Address.String())
	require.Equal(t, `module.lab.network.dmz`, stored.Deps[0].String())
}

func TestAllAndTopoSortAreDeterministic(t *testing.T) {
	g := New()
	for _, addr := range []address.Address{
		address.StringInstance("node", "web", "blue"),
		address.IntInstance("node", "web", 1),
		address.Resource("node", "web"),
		address.IntInstance("node", "web", 0),
	} {
		require.NoError(t, g.AddNode(addr, nil))
	}

	want := []address.Address{
		address.Resource("node", "web"),
		address.IntInstance("node", "web", 0),
		address.IntInstance("node", "web", 1),
		address.StringInstance("node", "web", "blue"),
	}
	require.Equal(t, want, nodeAddresses(g.All()))
	order, err := g.TopoSort()
	require.NoError(t, err)
	require.Equal(t, want, order)
}

func TestCycleDetection(t *testing.T) {
	g := New()
	a := address.Resource("a", "one")
	b := address.Resource("b", "one")
	require.NoError(t, g.AddNode(a, []address.Address{b}))
	require.NoError(t, g.AddNode(b, []address.Address{a}))

	_, err := g.TopoSort()
	require.ErrorContains(t, err, "cycle")
}

func TestUnknownReference(t *testing.T) {
	g := New()
	require.NoError(t, g.AddNode(address.Resource("node", "web"), []address.Address{address.Resource("missing", "x")}))

	_, err := g.TopoSort()
	require.ErrorContains(t, err, "unknown")
}

func TestReverseWalk(t *testing.T) {
	g := New()
	network := address.Resource("network", "dmz")
	web := address.Resource("node", "web")
	require.NoError(t, g.AddNode(network, nil))
	require.NoError(t, g.AddNode(web, []address.Address{network}))

	order, err := g.ReverseTopoSort()
	require.NoError(t, err)
	require.Less(t, indexOf(order, web), indexOf(order, network))
}

func indexOf(order []address.Address, want address.Address) int {
	for i, addr := range order {
		if addr.Equal(want) {
			return i
		}
	}
	return -1
}

func nodeAddresses(nodes []*Node) []address.Address {
	out := make([]address.Address, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.Address)
	}
	return out
}
