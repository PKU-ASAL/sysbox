package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type observationProvider struct {
	status   state.ResourceStatus
	statuses map[string]state.ResourceStatus
}

func (p observationProvider) Type() string           { return "test_observation" }
func (p observationProvider) Schema() ResourceSchema { return ResourceSchemaFor(p.Type()) }
func (p observationProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	status := p.status
	if selected := p.statuses[current.Address.Name]; selected != "" {
		status = selected
	}
	return ResourceReadResult{Status: status}, nil
}
func (p observationProvider) PlanDiff(n *graph.Node, _ *state.Resource) (controlplane.PlannedChange, error) {
	return controlplane.PlannedChange{Address: n.Address, Action: controlplane.PlanActionNoop}, nil
}
func (p observationProvider) Create(context.Context, *ProviderContext, *graph.Node) (state.Resource, error) {
	return state.Resource{}, nil
}
func (p observationProvider) Delete(context.Context, *ProviderContext, state.Resource) error {
	return nil
}
func (p observationProvider) ExternalID(state.Resource) string { return "" }

func isolateHandlerRegistry(t *testing.T) {
	t.Helper()
	previous := resourceHandlers
	resourceHandlers = newHandlerRegistry()
	t.Cleanup(func() { resourceHandlers = previous })
}

func TestRefreshMapsExplicitObservationStatus(t *testing.T) {
	addr := address.Resource("test_observation", "item")
	g := graph.New()
	require.NoError(t, g.AddNode(addr, nil))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: addr, Status: state.ResourcePresent}}}
	plan := &Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionNoop}}}

	for _, tc := range []struct {
		status state.ResourceStatus
		action controlplane.PlanActionType
	}{
		{state.ResourcePresent, controlplane.PlanActionNoop}, {state.ResourceAbsent, controlplane.PlanActionReplace},
		{state.ResourceDrifted, controlplane.PlanActionReplace}, {state.ResourceDegraded, controlplane.PlanActionNoop},
		{state.ResourceUnknown, controlplane.PlanActionUnknown},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			isolateHandlerRegistry(t)
			RegisterResourceHandler(observationProvider{status: tc.status})
			refreshed, err := NewExecutor(g, st).Refresh(context.Background(), plan)
			require.NoError(t, err)
			require.Equal(t, tc.action, refreshed.Actions[0].Action)
			require.Equal(t, tc.status, st.FindResource(addr).Status)
		})
	}
}

func TestRefreshDoesNotCascadeDependencyReplacement(t *testing.T) {
	isolateHandlerRegistry(t)
	dep := address.Resource("test_observation", "dep")
	child := address.Resource("test_observation", "child")
	g := graph.New()
	require.NoError(t, g.AddNode(dep, nil))
	require.NoError(t, g.AddNode(child, []address.Address{dep}))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: dep}, {Address: child}}}
	plan := &Plan{Actions: []controlplane.PlannedChange{{Address: dep, Action: controlplane.PlanActionNoop}, {Address: child, Action: controlplane.PlanActionNoop}}}
	RegisterResourceHandler(observationProvider{statuses: map[string]state.ResourceStatus{"dep": state.ResourceAbsent, "child": state.ResourcePresent}})
	refreshed, err := NewExecutor(g, st).Refresh(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, refreshed.Actions[0].Action)
	require.Equal(t, controlplane.PlanActionNoop, refreshed.Actions[1].Action)
}
