package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestApplyCannotTreatIncompleteRetainedResourceAsReady(t *testing.T) {
	e, h, addr := convergenceFixture(t, state.ResourcePresent, false)
	r := state.Resource{Address: addr, OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 2}
	require.NoError(t, r.RequireRecovery("decision", r.ResourceUID, r.Generation))
	e.state.Resources = []state.Resource{r}
	err := e.Apply(context.Background(), singleConvergencePlan(addr, model.PlanActionRead))
	require.ErrorIs(t, err, ErrActionUncertain)
	require.Zero(t, h.creates)
	require.Zero(t, h.deletes)
}

func TestExplicitResetClearsRecoveryRequirementOnlyAfterSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			sub := &portTestSubstrate{name: "recovery-reset", coldPlug: true}
			if fail {
				sub.nodeObservation = substrate.NodeObservation{Exists: true, Running: true, Healthy: false, Status: substrate.NodeStatusUnhealthy}
			}
			registerPortTestDriver(t, sub)
			graph, st := configuredResetTestState(t, sub, "first")
			e := testActionExecutor(t, graph, st)
			node := st.FindResource(address.Resource("sysbox_node", "first"))
			require.NoError(t, node.RequireRecovery("decision", node.ResourceUID, node.Generation))
			saved := e.actions.(*memoryActions).state.FindResource(node.Address)
			require.NoError(t, saved.RequireRecovery("decision", saved.ResourceUID, saved.Generation))
			plan, err := BuildResetPlan(graph, st, "sysbox_node.first")
			require.NoError(t, err)
			err = e.Reset(context.Background(), plan)
			if fail {
				require.Error(t, err)
				require.True(t, st.FindResource(node.Address).RecoveryRequired())
			} else {
				require.NoError(t, err)
				require.False(t, st.FindResource(node.Address).RecoveryRequired())
			}
		})
	}
}
