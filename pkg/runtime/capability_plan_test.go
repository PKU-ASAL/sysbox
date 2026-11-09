package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestComputePlanRejectsMissingCapabilityBeforeMutation(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	g := graph.New()
	image := address.Resource("sysbox_image", "base")
	require.NoError(t, g.AddNode(image, nil))
	g.Get(image).Data = &config.ImageConfig{Substrate: "missing", Kind: "oci", Source: "example.invalid/base:latest", Architecture: "amd64", GuestFamily: "linux"}
	addr := address.Resource("sysbox_node", "target")
	require.NoError(t, g.AddNode(addr, []address.Address{image}))
	g.Get(addr).Data = &config.NodeConfig{Substrate: "missing", Image: "sysbox_image.base"}
	_, err := ComputePlan(g, &state.State{Version: state.SchemaVersion}, driver.DefaultRegistry)
	require.ErrorContains(t, err, "driver is not registered")
}

func TestFirecrackerMultipleLinksUseOneFabric(t *testing.T) {
	registerPortTestDriver(t, &portTestSubstrate{name: "firecracker"})
	n := &graph.Node{Address: address.Resource("sysbox_node", "vm"), Data: &config.NodeConfig{Substrate: "firecracker", Links: []config.LinkConfig{{Name: "a"}, {Name: "b"}}}}
	_, err := (NodeResourceHandler{}).RequiredCapabilities(n, driver.DefaultRegistry)
	require.NoError(t, err)
}
func TestFirecrackerRejectsAmbiguousDefaultGateways(t *testing.T) {
	n := &graph.Node{Address: address.Resource("sysbox_node", "vm"), Data: &config.NodeConfig{Substrate: "firecracker", Links: []config.LinkConfig{{Name: "a", Gateway: "10.1.0.1"}, {Name: "b", Gateway: "10.2.0.1"}}}}
	_, err := (NodeResourceHandler{}).RequiredCapabilities(n, driver.DefaultRegistry)
	require.ErrorContains(t, err, "default gateway")
}
