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

func TestComputePlanUsesTopologicalActionOrder(t *testing.T) {
	g := graph.New()
	network := address.Resource("sysbox_network", "dmz")
	node := address.Resource("sysbox_node", "web")
	require.NoError(t, g.AddNode(network, nil))
	require.NoError(t, g.AddNode(node, []address.Address{network}))
	plan, err := ComputePlan(g, &state.State{Version: state.SchemaVersion})
	require.NoError(t, err)
	require.Equal(t, []controlplane.PlannedChange{
		{Address: network, Action: controlplane.PlanActionCreate, Reason: "resource not present in state"},
		{Address: node, Action: controlplane.PlanActionCreate, Reason: "resource not present in state"},
	}, plan.Actions)
}

func TestComputePlanDeletesOrphans(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "orphan")})
	plan, err := ComputePlan(graph.New(), st)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionDelete, plan.Actions[0].Action)
}

func TestComputePlanRejectsPreventDestroy(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "protected"), Attributes: map[string]any{"lifecycle_prevent_destroy": true}})
	_, err := ComputePlan(graph.New(), st)
	require.ErrorContains(t, err, "prevent_destroy blocks deletion")
}

func TestPlanDiffReportsReplacementFields(t *testing.T) {
	addr := address.Resource("sysbox_network", "dmz")
	g := graph.New()
	require.NoError(t, g.AddNode(addr, nil))
	require.NoError(t, g.SetData(addr, &config.NetworkConfig{CIDR: "10.0.2.0/24"}))
	node := g.Get(addr)
	hash, err := desiredHash(node)
	require.NoError(t, err)
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: addr, Attributes: map[string]any{"desired_hash": hash, "desired": map[string]any{"cidr": "10.0.1.0/24"}}})
	// Force a different desired hash while retaining comparable prior desired values.
	st.Resources[0].Attributes["desired_hash"] = "stale"
	plan, err := ComputePlan(g, st)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, plan.Actions[0].Action)
	require.Contains(t, plan.Actions[0].Changes, "cidr")
}

func TestRefreshReturnsNewPlanAndMarksDriftUnknown(t *testing.T) {
	addr := address.Resource("sysbox_kernel", "linux")
	g := graph.New()
	require.NoError(t, g.AddNode(addr, nil))
	original := &Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionNoop}}}
	refreshed, err := NewExecutor(g, &state.State{Version: state.SchemaVersion}).Refresh(context.Background(), original)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, original.Actions[0].Action)
	require.Equal(t, controlplane.PlanActionReplace, refreshed.Actions[0].Action)
}

func TestPlanValidateRejectsUnsupportedAction(t *testing.T) {
	plan := Plan{Actions: []controlplane.PlannedChange{{Address: address.Resource("sysbox_node", "web"), Action: "update"}}}
	require.ErrorContains(t, plan.Validate(), "unsupported plan action")
}

func TestFilterPlanByTargetUsesCanonicalAddress(t *testing.T) {
	web := address.StringInstance("sysbox_node", "web", "blue")
	db := address.Resource("sysbox_node", "db")
	plan := &Plan{Actions: []controlplane.PlannedChange{{Address: web, Action: controlplane.PlanActionCreate}, {Address: db, Action: controlplane.PlanActionCreate}}}
	filtered := FilterPlanByTarget(plan, "sysbox_node", "web")
	require.Equal(t, controlplane.PlanActionCreate, filtered.Actions[0].Action)
	require.Equal(t, controlplane.PlanActionNoop, filtered.Actions[1].Action)
}
