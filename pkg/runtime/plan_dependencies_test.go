package runtime

import (
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
)

type replacementPlanner struct{ convergenceHandler }

func (*replacementPlanner) PlanDiff(n *graph.Node, r *state.Resource) (model.PlannedChange, error) {
	action := model.PlanActionNoop
	if n.Address.Name == "parent" {
		action = model.PlanActionReplace
	}
	return model.PlannedChange{Address: n.Address, Action: action}, nil
}
func TestPlanPreviewRejectsIncompleteDependencyReplacement(t *testing.T) {
	isolateHandlerRegistry(t)
	h := &replacementPlanner{}
	RegisterResourceHandler(h)
	parent := address.Resource(h.Type(), "parent")
	child := address.Resource(h.Type(), "child")
	g := graph.New()
	require.NoError(t, g.AddNode(parent, nil))
	require.NoError(t, g.AddNode(child, []address.Address{parent}))
	st := &state.State{Resources: []state.Resource{{Address: parent}, {Address: child, Dependencies: []address.Address{parent}}}}
	plan, err := ComputePlan(g, st, driver.NewRegistry())
	require.NoError(t, err)
	err = plan.ValidateDependencies(st)
	require.ErrorContains(t, err, "still depends on destructive target")
	require.Zero(t, h.creates)
	require.Zero(t, h.deletes)
}

func TestTargetPlanIgnoresUnselectedReplacementConflict(t *testing.T) {
	isolateHandlerRegistry(t)
	h := &replacementPlanner{}
	RegisterResourceHandler(h)
	parent, child, target := address.Resource(h.Type(), "parent"), address.Resource(h.Type(), "child"), address.Resource(h.Type(), "independent")
	g := graph.New()
	require.NoError(t, g.AddNode(parent, nil))
	require.NoError(t, g.AddNode(child, []address.Address{parent}))
	require.NoError(t, g.AddNode(target, nil))
	st := &state.State{Resources: []state.Resource{{Address: parent}, {Address: child, Dependencies: []address.Address{parent}}, {Address: target}}}
	plan, err := ComputePlan(g, st, driver.NewRegistry())
	require.NoError(t, err)
	require.Error(t, plan.ValidateDependencies(st))
	selected := FilterPlanByTarget(plan, target.Type, target.Name)
	require.NoError(t, selected.ValidateDependencies(st))
}
