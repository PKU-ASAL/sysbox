package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

type continuationActions struct {
	*memoryActions
	receipt *ResetContinuationReceipt
	reject  bool
}

func (m *continuationActions) LoadResetContinuation(context.Context, string, string) (*ResetContinuationReceipt, error) {
	if m.receipt == nil {
		return nil, ErrResetContinuationNotFound
	}
	return m.receipt, nil
}

func (m *continuationActions) CommitResetContinuation(ctx context.Context, req ResetContinuationRequest) (*ResetContinuationReceipt, error) {
	if m.reject {
		return nil, errors.New("continuation commit unavailable")
	}
	if m.receipt != nil {
		return m.receipt, nil
	}
	for _, a := range req.Missing {
		if err := m.CommitActionIntent(ctx, a); err != nil {
			return nil, err
		}
	}
	m.receipt = &ResetContinuationReceipt{Request: req, AdmittedAt: time.Now().UTC()}
	return m.receipt, nil
}

func TestResetContinuationAdmitsBeforeEffectsAndReusesReceipt(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	second := st.FindResource(address.Resource("sysbox_node", "second"))
	second.Driver = sub.name
	second.Attributes = state.MustAttributes(map[string]any{"guest_family": "linux"})
	require.NoError(t, second.SetRuntimeValue("container_id", "old-second"))
	original := testActionExecutor(t, g, st)
	queued := time.Now().UTC()
	original.SetRunIncarnation(queued, "config-revision")
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	digest, err := planActionsSHA256(plan.Actions)
	require.NoError(t, err)
	cp := &OperationCheckpoint{RunID: original.runID, Topology: original.topology, Operation: "reset", Plan: plan.Actions, PlanSHA256: digest}
	cp.Steps = []OperationStep{{Kind: "resource", Resource: plan.Actions[0].Address.String(), Action: model.PlanActionReset, Status: OperationDone}}
	first, err := original.newAction(context.Background(), plan.Actions[0])
	require.NoError(t, err)
	first.Phase = "accepted"
	m := &continuationActions{memoryActions: original.actions.(*memoryActions)}
	require.NoError(t, m.CommitActionIntent(context.Background(), first))
	cold := NewExecutor(g, st, driver.DefaultRegistry)
	cold.SetRunContext(original.topology, "continuation-execution")
	cold.SetActionRepository(m)
	req, err := cold.PrepareResetContinuation(context.Background(), cp, queued, "config-revision")
	require.NoError(t, err)
	require.Len(t, req.Existing, 1)
	require.Len(t, req.Missing, 1)
	require.Equal(t, original.runID, req.Missing[0].RunID)
	require.Equal(t, queued, req.Missing[0].RunQueuedAt)
	require.Empty(t, sub.resetLifecycle)
	m.reject = true
	require.ErrorContains(t, cold.ContinueReset(context.Background(), *req), "continuation commit unavailable")
	require.Empty(t, sub.resetLifecycle)
	m.reject = false
	require.NoError(t, cold.ContinueReset(context.Background(), *req))
	require.Len(t, m.actions, 2)
	for _, action := range m.actions {
		require.Equal(t, ActionSucceeded, action.Status)
	}
	prepares := 0
	for _, event := range sub.resetLifecycle {
		if event == "prepare:old-first" || event == "prepare:old-second" {
			prepares++
		}
	}
	require.Equal(t, 2, prepares, "checkpoint step completion cannot override pending durable actions")
	before := append([]string(nil), sub.resetLifecycle...)
	coldAgain := NewExecutor(g, st, driver.DefaultRegistry)
	coldAgain.SetRunContext(original.topology, "another-process")
	coldAgain.SetActionRepository(m)
	retry, err := coldAgain.PrepareResetContinuation(context.Background(), cp, queued, "config-revision")
	require.NoError(t, err)
	require.Equal(t, req, retry)
	require.NoError(t, coldAgain.ContinueReset(context.Background(), *retry))
	require.Equal(t, before, sub.resetLifecycle)
	// Baseline equality alone is insufficient: reject a changed node config.
	g.Get(second.Address).Data.(*config.NodeConfig).Env = map[string]string{"CHANGED": "yes"}
	_, err = coldAgain.PrepareResetContinuation(context.Background(), cp, queued, "config-revision")
	require.ErrorIs(t, err, ErrPlanStale)
	require.Equal(t, before, sub.resetLifecycle)
}
