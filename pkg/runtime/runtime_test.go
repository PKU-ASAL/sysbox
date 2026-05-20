package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestPlanAddsNewResources(t *testing.T) {
	g := graph.New()
	g.AddNode("sysbox_network", "dmz", nil)
	g.AddNode("sysbox_node", "web", []graph.Ref{{Type: "sysbox_network", Name: "dmz"}})

	s := &state.State{Version: state.SchemaVersion}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Len(t, plan.Add, 2)
	require.Empty(t, plan.Destroy)
}

func TestRefreshCascadesChangedDependents(t *testing.T) {
	g := graph.New()
	g.AddNode("sysbox_network", "dmz", nil)
	g.AddNode("sysbox_node", "web", []graph.Ref{{Type: "sysbox_network", Name: "dmz"}})
	g.AddNode("sysbox_actor", "agent", []graph.Ref{{Type: "sysbox_node", Name: "web"}})

	s := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{Type: "sysbox_network", Name: "dmz", Provider: "network", Instance: map[string]any{"netns": "missing-netns", "bridge": "br-dmz"}},
			{Type: "sysbox_node", Name: "web", Provider: "docker", Instance: map[string]any{"container_id": "web"}},
			{Type: "sysbox_actor", Name: "agent", Provider: "docker", Instance: map[string]any{}},
		},
	}
	plan := &Plan{Unchanged: []graph.NodeID{
		{Type: "sysbox_network", Name: "dmz"},
		{Type: "sysbox_node", Name: "web"},
		{Type: "sysbox_actor", Name: "agent"},
	}}

	NewExecutor(g, s).Refresh(context.Background(), plan)

	require.ElementsMatch(t, []graph.NodeID{
		{Type: "sysbox_network", Name: "dmz"},
		{Type: "sysbox_node", Name: "web"},
		{Type: "sysbox_actor", Name: "agent"},
	}, plan.Change)
	require.Empty(t, plan.Unchanged)
}

func TestPlanDetectsDestroys(t *testing.T) {
	g := graph.New()

	s := &state.State{
		Version: state.SchemaVersion,
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
		Version: state.SchemaVersion,
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

func TestPlanDetectsDesiredHashChange(t *testing.T) {
	g := graph.New()
	n := g.AddNode("sysbox_network", "dmz", nil)
	n.Data = &config.NetworkConfig{CIDR: "10.0.1.0/24"}
	oldHash, err := desiredHash(n)
	require.NoError(t, err)
	oldPayload, _ := desiredPayload(n)

	n.Data = &config.NetworkConfig{CIDR: "10.0.2.0/24"}
	s := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{
				Type:     "sysbox_network",
				Name:     "dmz",
				Provider: "network",
				Instance: map[string]any{
					"cidr":            "10.0.1.0/24",
					desiredHashKey:    oldHash,
					desiredPayloadKey: oldPayload,
				},
			},
		},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Empty(t, plan.Add)
	require.Empty(t, plan.Destroy)
	require.Empty(t, plan.Unchanged)
	require.Equal(t, []graph.NodeID{{Type: "sysbox_network", Name: "dmz"}}, plan.Change)
	require.Len(t, plan.Actions, 1)
	require.Equal(t, PlanActionReplace, plan.Actions[0].Action)
	require.Equal(t, "10.0.1.0/24", plan.Actions[0].Changes["cidr"].Before)
	require.Equal(t, "10.0.2.0/24", plan.Actions[0].Changes["cidr"].After)
	require.True(t, plan.Actions[0].Changes["cidr"].RequiresReplace)
}

func TestPlanRedactsSensitiveDiffFields(t *testing.T) {
	g := graph.New()
	n := g.AddNode("sysbox_node", "web", nil)
	n.Data = &config.NodeConfig{Image: "sysbox_image.alpine.id", Substrate: "docker", Env: map[string]string{"TOKEN": "old"}}
	oldHash, err := desiredHash(n)
	require.NoError(t, err)
	oldPayload, _ := desiredPayload(n)

	n.Data = &config.NodeConfig{Image: "sysbox_image.alpine.id", Substrate: "docker", Env: map[string]string{"TOKEN": "new"}}
	s := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Type:     "sysbox_node",
			Name:     "web",
			Provider: "docker",
			Instance: map[string]any{
				desiredHashKey:    oldHash,
				desiredPayloadKey: oldPayload,
			},
		}},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Len(t, plan.Change, 1)
	envChange := plan.Actions[0].Changes["env"]
	require.True(t, envChange.Sensitive)
	require.Equal(t, "(sensitive)", envChange.Before)
	require.Equal(t, "(sensitive)", envChange.After)
}

func TestPlanKeepsMatchingDesiredHashUnchanged(t *testing.T) {
	g := graph.New()
	n := g.AddNode("sysbox_network", "dmz", nil)
	n.Data = &config.NetworkConfig{CIDR: "10.0.1.0/24"}
	hash, err := desiredHash(n)
	require.NoError(t, err)

	s := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{
				Type:     "sysbox_network",
				Name:     "dmz",
				Provider: "network",
				Instance: map[string]any{desiredHashKey: hash},
			},
		},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Empty(t, plan.Add)
	require.Empty(t, plan.Destroy)
	require.Empty(t, plan.Change)
	require.Equal(t, []graph.NodeID{{Type: "sysbox_network", Name: "dmz"}}, plan.Unchanged)
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

	name := config.ResolveName("sysbox_image.alpine.id")
	require.Equal(t, "alpine", name)

	name = config.ResolveName("sysbox_network.dmz.id")
	require.Equal(t, "dmz", name)

	// Bare names pass through unchanged.
	name = config.ResolveName("alpine")
	require.Equal(t, "alpine", name)

	// Empty string returns empty.
	name = config.ResolveName("")
	require.Equal(t, "", name)
}
