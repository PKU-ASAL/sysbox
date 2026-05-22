package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestNodeResourceProviderRegistered(t *testing.T) {
	p, ok := GetResourceProvider("sysbox_node")
	require.True(t, ok)
	require.Equal(t, "sysbox_node", p.Type())
	require.Equal(t, "sysbox_node", p.Schema().Type)
}

func TestNodeResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		ID: graph.NodeID{Type: "sysbox_node", Name: "web"},
		Data: &config.NodeConfig{
			Image:     "sysbox_image.alpine.id",
			Substrate: "docker",
			Env:       map[string]string{"TOKEN": "old"},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Type: "sysbox_node", Name: "web", Provider: "docker", Instance: inst}
	p := NodeResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, PlanActionNoop, action.Action)

	n.Data = &config.NodeConfig{
		Image:     "sysbox_image.alpine.id",
		Substrate: "docker",
		Env:       map[string]string{"TOKEN": "new"},
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, PlanActionReplace, action.Action)
	require.True(t, action.Changes["env"].Sensitive)
}

func TestNodeResourceProviderDeleteMissingSubstrateReturnsError(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{
		Type:     "sysbox_node",
		Name:     "web",
		Provider: "missing-node-provider",
		Instance: map[string]any{"container_id": "node"},
	}

	err := NodeResourceProvider{}.Delete(context.Background(), &ProviderContext{exec: exec}, res)
	require.Error(t, err)
}
