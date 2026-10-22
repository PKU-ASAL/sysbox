package runtime

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOwningAgentObserveAndRecoverLostCommit(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	repo := e.actions.(*memoryActions)
	repo.patchErr = errors.New("commit lost")
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, controlplane.PlanActionCreate)))
	require.Equal(t, 1, h.creates)
	repo.patchErr = nil
	_, observed, err := ObserveRunActions(context.Background(), repo, "test-topology", "run-1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.Equal(t, "present", observed[0].Status)
	require.Equal(t, repo.actions[0].ResourceUID, observed[0].ResourceUID)
	require.NoError(t, e.RecoverRunActions(context.Background(), "run-1"))
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
	require.Equal(t, ActionSucceeded, repo.actions[0].Status)
}
func TestOwningAgentRecoveryUnknownCannotReplay(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	h.afterCreate = state.ResourceUnknown
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, controlplane.PlanActionCreate)))
	_, observed, err := ObserveRunActions(context.Background(), e.actions, "test-topology", "run-1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.Equal(t, "unknown", observed[0].Status)
	require.Error(t, e.RecoverRunActions(context.Background(), "run-1"))
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
	require.Error(t, e.RecoverRunActions(context.Background(), "foreign-run"))
}

func TestResetObservationAccountsForSourceAndReplacementGenerations(t *testing.T) {
	e, _, addr := convergenceFixture(t, state.ResourcePresent, false)
	repo := e.actions.(*memoryActions)
	source := state.Resource{Address: addr, Driver: "fixture", ExternalID: "old", OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 1}
	replacement := source
	replacement.ExternalID = "new"
	replacement.Generation = 2
	repo.actions = []ActionRecord{{ID: "reset", Topology: "test-topology", RunID: "run-1", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 2, Candidate: &source, Replacement: &replacement, Phase: "applying", ReplayForbidden: true}}
	_, observed, err := ObserveRunActions(context.Background(), repo, "test-topology", "run-1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.NotNil(t, observed[0].ResetSource)
	require.NotNil(t, observed[0].ResetReplacement)
	require.Equal(t, int64(1), observed[0].ResetSource.Generation)
	require.Equal(t, int64(2), observed[0].ResetReplacement.Generation)
	require.Equal(t, "old", observed[0].ResetSource.ExternalID)
	require.Equal(t, "new", observed[0].ResetReplacement.ExternalID)
	require.Equal(t, int64(2), observed[0].Generation)
	require.Equal(t, "unknown", observed[0].Status, "composite reset success is never inferred")
}

func TestNewResetAfterSourceDispositionUsesNextActualGeneration(t *testing.T) {
	e, _, addr := convergenceFixture(t, state.ResourcePresent, false)
	repo := e.actions.(*memoryActions)
	source := state.Resource{Address: addr, Driver: "fixture", ExternalID: "old", OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 1}
	e.state.Resources = []state.Resource{source}
	repo.actions = []ActionRecord{{ID: "reset-old", RunID: "old-run", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 2, Candidate: &source, Phase: "destroying", ReplayForbidden: true, DispositionID: "intent:explicit-source-retention"}}
	next, err := e.newAction(context.Background(), controlplane.PlannedChange{Address: addr, Action: controlplane.PlanActionReset})
	require.NoError(t, err)
	require.Equal(t, int64(2), next.Generation, "the fenced abandoned reset never created generation 2")
	require.Equal(t, int64(1), next.Candidate.Generation)
	repo.actions[0].Phase = "applying"
	next, err = e.newAction(context.Background(), controlplane.PlannedChange{Address: addr, Action: controlplane.PlanActionReset})
	require.NoError(t, err)
	require.Greater(t, next.Generation, int64(2), "unknown replacement must not reuse a generation")
}

func TestReadAndDeleteAfterResetDispositionUseActualSourceGeneration(t *testing.T) {
	for _, kind := range []controlplane.PlanActionType{controlplane.PlanActionRead, controlplane.PlanActionDelete} {
		t.Run(string(kind), func(t *testing.T) {
			e, _, addr := convergenceFixture(t, state.ResourcePresent, false)
			repo := e.actions.(*memoryActions)
			source := state.Resource{Address: addr, Driver: "fixture", ExternalID: "old", OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 1}
			e.state.Resources = []state.Resource{source}
			repo.actions = []ActionRecord{{ID: "reset-old", RunID: "old-run", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 2, Candidate: &source, Phase: "destroying", ReplayForbidden: true, DispositionID: "intent:source-retention"}}
			next, err := e.newAction(context.Background(), controlplane.PlannedChange{Address: addr, Action: kind})
			require.NoError(t, err)
			require.Equal(t, int64(1), next.Generation)
			require.Equal(t, next.Candidate.Generation, next.Generation)
		})
	}
}
