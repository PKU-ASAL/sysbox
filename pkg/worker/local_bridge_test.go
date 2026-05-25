package worker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestLocalBridgeFilterApplyPlanByTarget(t *testing.T) {
	bridge := NewLocalBridge(LocalOptions{Target: "sysbox_node.web"})
	plan := &runtime.Plan{
		Add: []graph.NodeID{
			{Type: "sysbox_network", Name: "shared"},
			{Type: "sysbox_node", Name: "web"},
		},
		Actions: []runtime.PlanAction{
			{Resource: "sysbox_network.shared", Type: "sysbox_network", Name: "shared", Action: runtime.PlanActionCreate},
			{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Action: runtime.PlanActionCreate},
		},
	}

	filtered, err := bridge.FilterApplyPlan(plan)
	require.NoError(t, err)
	var creates []runtime.PlanAction
	for _, action := range filtered.Actions {
		if action.Action == runtime.PlanActionCreate {
			creates = append(creates, action)
		}
	}
	require.Len(t, creates, 1)
	require.Equal(t, "sysbox_node.web", creates[0].Resource)
}

func TestLocalBridgeBuildDestroyPlanHonorsPreventDestroy(t *testing.T) {
	bridge := NewLocalBridge(LocalOptions{})
	st := &state.State{Resources: []state.Resource{
		{Type: "sysbox_node", Name: "web", Instance: map[string]any{}},
		{Type: "sysbox_node", Name: "db", Instance: map[string]any{"lifecycle_prevent_destroy": true}},
	}}

	plan, err := bridge.BuildDestroyPlan(st)
	require.NoError(t, err)
	require.Len(t, plan.Destroy, 1)
	require.Equal(t, "web", plan.Destroy[0].Name)
	require.Len(t, plan.Protected, 1)
	require.Equal(t, "db", plan.Protected[0].Name)
	require.Len(t, plan.Actions, 2)
}
