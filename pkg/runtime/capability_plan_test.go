package runtime

import (
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestComputePlanRejectsMissingCapabilityBeforeMutation(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	g := graph.New()
	addr := address.Resource("sysbox_node", "target")
	require.NoError(t, g.AddNode(addr, nil))
	g.Get(addr).Data = &config.NodeConfig{Substrate: "missing", Image: "sysbox_image.base"}
	_, err := ComputePlan(g, &state.State{Version: state.SchemaVersion})
	require.ErrorContains(t, err, "driver is not registered")
}
