package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestPlanAddsNewResources(t *testing.T) {
	g := graph.New()
	g.AddNode("sysbox_network", "dmz", nil)
	g.AddNode("sysbox_node", "web", []graph.Ref{{Type: "sysbox_network", Name: "dmz"}})

	s := &state.State{Version: 1}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Len(t, plan.Add, 2)
	require.Empty(t, plan.Destroy)
}

func TestPlanDetectsDestroys(t *testing.T) {
	g := graph.New()

	s := &state.State{
		Version: 1,
		Resources: []state.Resource{
			{Type: "sysbox_node", Name: "orphan", Provider: "docker"},
		},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Len(t, plan.Destroy, 1)
	require.Equal(t, "orphan", plan.Destroy[0].Name)
}

func TestPlanPassesThroughUnchanged(t *testing.T) {
	g := graph.New()
	g.AddNode("sysbox_network", "dmz", nil)

	s := &state.State{
		Version: 1,
		Resources: []state.Resource{
			{Type: "sysbox_network", Name: "dmz", Provider: "network", Instance: map[string]any{"netns": "sysbox-net-dmz"}},
		},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Empty(t, plan.Add)
	require.Empty(t, plan.Destroy)
	require.Len(t, plan.Unchanged, 1)
}

func TestPlanSummary(t *testing.T) {
	p := &Plan{
		Add:       []graph.NodeID{{Type: "x", Name: "y"}},
		Destroy:   []state.Resource{{Type: "a", Name: "b"}},
		Unchanged: nil,
	}
	require.True(t, p.HasChanges())
	require.Contains(t, p.Summary(), "1 to add")
	require.Contains(t, p.Summary(), "1 to destroy")
}

func TestResolveRefs(t *testing.T) {
	s, err := resolveSubstrateRef("docker")
	require.NoError(t, err)
	require.Equal(t, "docker", s)

	s, err = resolveSubstrateRef("substrate.docker.light")
	require.NoError(t, err)
	require.Equal(t, "docker", s)

	_, err = resolveSubstrateRef("a.b")
	require.Error(t, err)

	name, err := resolveImageRef("sysbox_image.alpine.id")
	require.NoError(t, err)
	require.Equal(t, "alpine", name)

	name, err = resolveNetworkRef("sysbox_network.dmz.id")
	require.NoError(t, err)
	require.Equal(t, "dmz", name)
}
