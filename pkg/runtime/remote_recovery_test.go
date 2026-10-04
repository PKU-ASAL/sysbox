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
