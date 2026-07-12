package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestDataResourceProvidersRegistered(t *testing.T) {
	for _, typ := range []string{"data_sysbox_node", "data_sysbox_network", "data_sysbox_image"} {
		p, ok := GetResourceProvider(typ)
		require.True(t, ok, typ)
		require.Equal(t, typ, p.Type())
		require.Equal(t, typ, p.Schema().Type)
	}
}

func TestDataResourceProviderPlanDiffReads(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("data_sysbox_image", "alpine"),
		Data:    &config.DataImageConfig{Substrate: "docker", DockerRef: "alpine:latest"},
	}
	p := DataImageResourceProvider{}

	action, err := p.PlanDiff(n, nil)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionRead, action.Action)
	require.Equal(t, "data source not present in state", action.Reason)

	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("data_sysbox_image", "alpine"), Driver: "docker", Attributes: inst}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.DataImageConfig{Substrate: "docker", DockerRef: "busybox:latest"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionRead, action.Action)
	require.Contains(t, action.Changes, "data")
}

func TestComputePlanSchedulesDataSourcesAsRead(t *testing.T) {
	g := graph.New()
	addr := address.Resource("data_sysbox_image", "alpine")
	require.NoError(t, g.AddNode(addr, nil))
	n := g.Get(addr)
	n.Data = &config.DataImageConfig{Substrate: "docker", DockerRef: "alpine:latest"}

	plan, err := ComputePlan(g, &state.State{Version: state.SchemaVersion})
	require.NoError(t, err)
	require.Equal(t, []address.Address{address.Resource("data_sysbox_image", "alpine")}, plan.Add)
	require.Len(t, plan.Actions, 1)
	require.Equal(t, controlplane.PlanActionRead, plan.Actions[0].Action)
	require.True(t, plan.HasChanges())
}

func TestDataResourceProviderDeleteRemovesState(t *testing.T) {
	res := state.Resource{Address: address.Resource("data_sysbox_node", "existing"), Attributes: map[string]any{"data_read": true}}
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{res}}
	exec := NewExecutor(graph.New(), st)

	require.NoError(t, DataNodeResourceProvider{}.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, st.FindResource(address.Resource("data_sysbox_node", "existing")))
}
