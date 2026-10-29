package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

func init() {
	_ = driver.DefaultRegistry.Register(driver.Descriptor{Name: "network", Version: "test", LinuxNetwork: fakeLinuxNetwork{}})
}

func TestComputePlanUsesTopologicalActionOrder(t *testing.T) {
	g := graph.New()
	network := address.Resource("sysbox_network", "dmz")
	node := address.Resource("sysbox_node", "web")
	require.NoError(t, g.AddNode(network, nil))
	require.NoError(t, g.AddNode(node, []address.Address{network}))
	plan, err := ComputePlan(g, &state.State{Version: state.SchemaVersion}, driver.DefaultRegistry)
	require.NoError(t, err)
	require.Equal(t, []model.PlannedChange{
		{Address: network, Action: model.PlanActionCreate, Reason: "resource not present in state"},
		{Address: node, Action: model.PlanActionCreate, Reason: "resource not present in state"},
	}, plan.Actions)
}

func TestComputePlanDeletesOrphans(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "orphan")})
	plan, err := ComputePlan(graph.New(), st, driver.DefaultRegistry)
	require.NoError(t, err)
	require.Equal(t, model.PlanActionDelete, plan.Actions[0].Action)
}

func TestComputePlanRejectsPreventDestroy(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "protected"), Attributes: map[string]any{"lifecycle_prevent_destroy": true}})
	_, err := ComputePlan(graph.New(), st, driver.DefaultRegistry)
	require.ErrorContains(t, err, "prevent_destroy blocks deletion")
}

func TestReplacementCanRemovePriorPreventDestroy(t *testing.T) {
	addr := address.Resource("sysbox_network", "lab")
	g := graph.New()
	require.NoError(t, g.AddNode(addr, nil))
	require.NoError(t, g.SetData(addr, &config.NetworkConfig{CIDR: "10.0.2.0/24"}))
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: addr, Attributes: map[string]any{
		"desired_hash": "stale", "desired": map[string]any{"cidr": "10.0.1.0/24"}, "lifecycle_prevent_destroy": true,
	}})

	plan, err := ComputePlan(g, st, driver.DefaultRegistry)
	require.NoError(t, err)
	require.Equal(t, model.PlanActionReplace, plan.Actions[0].Action)
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
	plan, err := ComputePlan(g, st, driver.DefaultRegistry)
	require.NoError(t, err)
	require.Equal(t, model.PlanActionReplace, plan.Actions[0].Action)
	_, ok := fieldChangeAt(plan.Actions[0].Changes, "cidr")
	require.True(t, ok)
}

func TestPlanDiffDoesNotUseDesiredHashAsSemanticInput(t *testing.T) {
	addr := address.Resource("sysbox_network", "dmz")
	g := graph.New()
	require.NoError(t, g.AddNode(addr, nil))
	require.NoError(t, g.SetData(addr, &config.NetworkConfig{CIDR: "10.0.1.0/24"}))
	payload, _ := desiredPayload(g.Get(addr))
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: addr, Attributes: map[string]any{"desired_hash": "deliberately-wrong", "desired": payload}})

	plan, err := ComputePlan(g, st, driver.DefaultRegistry)
	require.NoError(t, err)
	require.Equal(t, model.PlanActionNoop, plan.Actions[0].Action)
	require.Empty(t, plan.Actions[0].Changes)
	require.Empty(t, plan.Actions[0].Reason)
}

func TestRouterDesiredPayloadIncludesRoutes(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_router", "edge"),
		Data: &config.RouterConfig{
			Substrate: "docker",
			Image:     "sysbox_image.router",
			Routes: []config.RouteConfig{{
				Destination: "10.0.2.0/24",
				Via:         "10.0.1.254",
			}},
		},
	}
	first, err := desiredHash(n)
	require.NoError(t, err)
	n.Data.(*config.RouterConfig).Routes[0].Via = "10.0.1.253"
	second, err := desiredHash(n)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}

func TestDesiredHashNormalizesNilAndEmptyDependsOn(t *testing.T) {
	addr := address.Resource("sysbox_kernel", "linux")
	nilNode := &graph.Node{Address: addr, Data: &config.KernelConfig{Source: "/tmp/vmlinux"}}
	emptyNode := &graph.Node{Address: addr, Data: &config.KernelConfig{Source: "/tmp/vmlinux", DependsOn: []string{}}}

	nilHash, err := desiredHash(nilNode)
	require.NoError(t, err)
	emptyHash, err := desiredHash(emptyNode)
	require.NoError(t, err)
	require.Equal(t, emptyHash, nilHash)
	payload, _ := desiredPayload(nilNode)
	require.Equal(t, []string{}, payload["depends_on"])
}

func fieldChangeAt(changes []model.FieldChange, path string) (model.FieldChange, bool) {
	for _, change := range changes {
		if change.Path == path {
			return change, true
		}
	}
	return model.FieldChange{}, false
}

func TestRefreshReturnsNewPlanAndMarksDriftUnknown(t *testing.T) {
	addr := address.Resource("sysbox_kernel", "linux")
	g := graph.New()
	require.NoError(t, g.AddNode(addr, nil))
	original := &Plan{Actions: []model.PlannedChange{{Address: addr, Action: model.PlanActionNoop}}}
	refreshed, err := NewExecutor(g, &state.State{Version: state.SchemaVersion}, driver.DefaultRegistry).Refresh(context.Background(), original)
	require.NoError(t, err)
	require.Equal(t, model.PlanActionNoop, original.Actions[0].Action)
	require.Equal(t, model.PlanActionReplace, refreshed.Actions[0].Action)
}

func TestPlanValidateRejectsUnsupportedAction(t *testing.T) {
	plan := Plan{Actions: []model.PlannedChange{{Address: address.Resource("sysbox_node", "web"), Action: "update"}}}
	require.ErrorContains(t, plan.Validate(), "unsupported plan action")
}

func TestFilterPlanByTargetUsesCanonicalAddress(t *testing.T) {
	web := address.Resource("sysbox_node", "web")
	db := address.Resource("sysbox_node", "db")
	plan := &Plan{Actions: []model.PlannedChange{{Address: web, Action: model.PlanActionCreate}, {Address: db, Action: model.PlanActionCreate}}}
	filtered := FilterPlanByTarget(plan, "sysbox_node", "web")
	require.Equal(t, model.PlanActionCreate, filtered.Actions[0].Action)
	require.Equal(t, model.PlanActionNoop, filtered.Actions[1].Action)
}

func TestTargetFilterExcludesOtherInstances(t *testing.T) {
	root := address.Resource("sysbox_node", "web")
	child, err := address.Parse("module.child.sysbox_node.web")
	require.NoError(t, err)
	indexed, err := address.Parse("sysbox_node.web[0]")
	require.NoError(t, err)
	plan := &Plan{Actions: []model.PlannedChange{{Address: root, Action: model.PlanActionCreate}, {Address: child, Action: model.PlanActionCreate}, {Address: indexed, Action: model.PlanActionCreate}}}
	filtered := FilterPlanByTarget(plan, "sysbox_node", "web")
	require.Equal(t, model.PlanActionCreate, filtered.Actions[0].Action)
	require.Equal(t, model.PlanActionNoop, filtered.Actions[1].Action)
	require.Equal(t, model.PlanActionNoop, filtered.Actions[2].Action)
}
