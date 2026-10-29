package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

func TestRunningNodeDoesNotProveImageEntryCompleted(t *testing.T) {
	e, d, cfg, addr := nodeGuardExecutor(t, nil)
	require.Empty(t, cfg.Provisioners)
	change := model.PlannedChange{Address: addr, Action: model.PlanActionCreate}
	record, err := e.newAction(context.Background(), change)
	require.NoError(t, err)
	// The VM exists and is running, but the create composite lost its result
	// before recording whether the image's entry command was launched.
	record.Status = ActionUncertain
	record.Candidate = &state.Resource{Address: addr, Driver: d.name, ExternalID: "node-id", OwnerUID: record.OwnerUID, ResourceUID: record.ResourceUID, Generation: record.Generation}
	e.actions.(*memoryActions).actions = []ActionRecord{record}
	require.ErrorIs(t, e.Apply(context.Background(), &Plan{Actions: []model.PlannedChange{change}}), ErrActionUncertain)
	require.Nil(t, e.state.FindResource(addr))
	require.Zero(t, d.creates)
	require.Zero(t, d.entries)
	require.NotEqual(t, ActionSucceeded, e.actions.(*memoryActions).actions[0].Status)
}
func TestDeleteRecoveryDoesNotRequireGuestSetupCompletion(t *testing.T) {
	e, h, addr := convergenceFixture(t, state.ResourceAbsent, true)
	require.NoError(t, e.graph.SetData(addr, &config.NodeConfig{Provisioners: []config.ProvisionerConfig{{Type: "exec", Program: "work", Shell: "none"}}}))
	change := model.PlannedChange{Address: addr, Action: model.PlanActionDelete}
	record, err := e.newAction(context.Background(), change)
	require.NoError(t, err)
	record.Status = ActionUncertain
	e.actions.(*memoryActions).actions = []ActionRecord{record}
	require.ErrorIs(t, e.Destroy(context.Background(), &Plan{Actions: []model.PlannedChange{change}}), ErrActionUncertain)
	require.NotNil(t, e.state.FindResource(addr))
	require.Zero(t, h.deletes)
	require.Zero(t, h.creates)
	require.NotEqual(t, ActionSucceeded, e.actions.(*memoryActions).actions[0].Status)
}

func TestImageEntryErrorKeepsCreateCompositeUncertain(t *testing.T) {
	e, d, _, addr := nodeGuardExecutor(t, nil)
	want := errors.New("image entry response lost")
	d.entryErr = want
	plan := &Plan{Actions: []model.PlannedChange{{Address: addr, Action: model.PlanActionCreate}}}
	require.ErrorIs(t, e.Apply(context.Background(), plan), want)
	require.Nil(t, e.state.FindResource(addr))
	require.Equal(t, 1, d.entries)
	require.False(t, e.actions.(*memoryActions).actions[0].EffectComplete)
	require.ErrorIs(t, e.Apply(context.Background(), plan), ErrActionUncertain)
	require.Equal(t, 1, d.entries)
}
