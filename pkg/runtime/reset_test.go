package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func resetTestGraph(t *testing.T) (*graph.Graph, *state.State) {
	t.Helper()
	g := graph.New()
	image := address.Resource("sysbox_image", "base")
	first := address.Resource("sysbox_node", "first")
	second := address.Resource("sysbox_node", "second")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{Substrate: "fake", Kind: "rootfs", Source: "/base", Architecture: "amd64", GuestFamily: "linux"}))
	require.NoError(t, g.AddNode(first, []address.Address{image}))
	require.NoError(t, g.SetData(first, &config.NodeConfig{Substrate: "fake", Image: "sysbox_image.base.id"}))
	require.NoError(t, g.AddNode(second, []address.Address{image, first}))
	require.NoError(t, g.SetData(second, &config.NodeConfig{Substrate: "fake", Image: "sysbox_image.base.id", DependsOn: []string{"sysbox_node.first"}}))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{
		{Address: image, Attributes: state.MustAttributes(map[string]any{
			"image_id": "base-id", "kind": "rootfs", "source": "/base",
			"sha256":       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"architecture": "amd64", "guest_family": "linux",
		})},
		{Address: first}, {Address: second},
	}}
	return g, st
}

func TestExecutorResetUsesProviderLifecycleAndUpdatesState(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := resetTestGraph(t)
	image := st.FindResource(address.Resource("sysbox_image", "base"))
	image.Driver = sub.name
	image.Attributes = state.MustAttributes(map[string]any{
		"image_id": "base-id", "kind": "rootfs", "source": "/base",
		"sha256":       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"architecture": "amd64", "guest_family": "linux",
	})
	for _, name := range []string{"first", "second"} {
		node := st.FindResource(address.Resource("sysbox_node", name))
		node.Driver = sub.name
		node.Attributes = state.MustAttributes(map[string]any{"guest_family": "linux"})
		require.NoError(t, node.SetRuntimeValue("container_id", "old-"+name))
	}
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)

	err = testActionExecutor(t, g, st).Reset(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"prepare:old-second", "destroy", "prepare:old-first", "destroy", "apply", "observe", "cleanup", "apply", "observe", "cleanup"}, sub.resetLifecycle)
	require.Equal(t, "node-reset", st.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
	require.Equal(t, "node-reset", st.FindResource(address.Resource("sysbox_node", "second")).ContainerID())
	for _, name := range []string{"first", "second"} {
		node := st.FindResource(address.Resource("sysbox_node", name))
		runtimeState, runtimeErr := node.RuntimeState()
		require.NoError(t, runtimeErr)
		require.NotContains(t, runtimeState, "container_id")
		require.Equal(t, "node-reset", node.ExternalID)
	}
	require.GreaterOrEqual(t, sub.nodeObserveCalls, 8, "reset observes old and new generations")
}

func TestExecutorResetRejectsUnhealthyFinalObservation(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-unhealthy", coldPlug: true, nodeObservation: substrate.NodeObservation{
		Exists: true, Running: true, Healthy: false, Status: substrate.NodeStatusUnhealthy, Reason: "health check failed",
	}}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)

	err = testActionExecutor(t, g, st).Reset(context.Background(), plan)
	require.ErrorContains(t, err, "final health")
	require.Equal(t, "old-first", st.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
}

func TestBuildResetPlanSupportsWholeTopologyAndExactNodeTarget(t *testing.T) {
	g, st := resetTestGraph(t)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	require.Equal(t, []model.PlannedChange{
		{Address: address.Resource("sysbox_node", "first"), Action: model.PlanActionReset, Reason: "reset mutable guest state from immutable baseline", Changes: []model.FieldChange{{Path: "baseline_digest", After: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{Address: address.Resource("sysbox_node", "second"), Action: model.PlanActionReset, Reason: "reset mutable guest state from immutable baseline", Changes: []model.FieldChange{{Path: "baseline_digest", After: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
	}, plan.Actions)

	targeted, err := BuildResetPlan(g, st, "sysbox_node.second")
	require.NoError(t, err)
	require.Len(t, targeted.Actions, 1)
	require.Equal(t, address.Resource("sysbox_node", "second"), targeted.Actions[0].Address)
}

func TestBuildResetPlanRejectsInvalidTargets(t *testing.T) {
	g, st := resetTestGraph(t)
	_, err := BuildResetPlan(g, st, "sysbox_network.lab")
	require.ErrorContains(t, err, "must be a sysbox_node")
	_, err = BuildResetPlan(g, st, "sysbox_node.missing")
	require.ErrorContains(t, err, "not declared")
}

func TestBuildResetPlanIgnoresPreventDestroyBecauseLogicalResourceSurvives(t *testing.T) {
	g, st := resetTestGraph(t)
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	node.Attributes = state.MustAttributes(map[string]any{"lifecycle_prevent_destroy": true})

	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.Len(t, plan.Actions, 1)
	require.Equal(t, model.PlanActionReset, plan.Actions[0].Action)
}

func TestResetPlanPinsBaselineAndRejectsDigestChangeBeforeMutation(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-pinned", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.Equal(t, "baseline_digest", plan.Actions[0].Changes[0].Path)
	require.Equal(t, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", plan.Actions[0].Changes[0].After)
	image := st.FindResource(address.Resource("sysbox_image", "base"))
	attrs := image.AttributeMap()
	attrs["sha256"] = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	image.Attributes = state.MustAttributes(attrs)

	err = testActionExecutor(t, g, st).Reset(context.Background(), plan)
	require.ErrorContains(t, err, "stale reset plan")
	require.Empty(t, sub.resetLifecycle)
}

func TestResumeResetPlanSkipsCompletedNodesAndKeepsFailedNodes(t *testing.T) {
	g, st := resetTestGraph(t)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	digest, err := planActionsSHA256(plan.Actions)
	require.NoError(t, err)
	cp := &OperationCheckpoint{Plan: append([]model.PlannedChange(nil), plan.Actions...), PlanSHA256: digest, Steps: []OperationStep{
		{Kind: "resource", Resource: "sysbox_node.first", Action: model.PlanActionReset, Status: OperationDone},
		{Kind: "resource", Resource: "sysbox_node.second", Action: model.PlanActionReset, Status: OperationFailed},
	}}

	resumed, err := ResumeResetPlan(cp, plan)
	require.NoError(t, err)
	require.Len(t, resumed.Actions, 1)
	require.Equal(t, "sysbox_node.second", resumed.Actions[0].Address.String())
}

func TestResumeResetPlanSupportsConsecutiveFailedRecoveryAttempts(t *testing.T) {
	g, st := resetTestGraph(t)
	full, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	fullDigest, err := planActionsSHA256(full.Actions)
	require.NoError(t, err)
	first := &OperationCheckpoint{Plan: full.Actions, PlanSHA256: fullDigest, Steps: []OperationStep{
		{Kind: "resource", Resource: "sysbox_node.first", Action: model.PlanActionReset, Status: OperationDone},
		{Kind: "resource", Resource: "sysbox_node.second", Action: model.PlanActionReset, Status: OperationFailed},
	}}
	pending, err := ResumeResetPlan(first, full)
	require.NoError(t, err)
	pendingDigest, err := planActionsSHA256(pending.Actions)
	require.NoError(t, err)
	second := &OperationCheckpoint{Plan: pending.Actions, PlanSHA256: pendingDigest, Steps: []OperationStep{
		{Kind: "resource", Resource: "sysbox_node.second", Action: model.PlanActionReset, Status: OperationFailed},
	}}

	resumedAgain, err := ResumeResetPlan(second, full)
	require.NoError(t, err)
	require.Equal(t, pending.Actions, resumedAgain.Actions)
}

func TestResumeResetPlanRejectsChangedPinnedBaseline(t *testing.T) {
	g, st := resetTestGraph(t)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	digest, err := planActionsSHA256(plan.Actions)
	require.NoError(t, err)
	cp := &OperationCheckpoint{Plan: append([]model.PlannedChange(nil), plan.Actions...), PlanSHA256: digest}
	changed := &Plan{Actions: append([]model.PlannedChange(nil), plan.Actions...)}
	changed.Actions[0].Changes = append([]model.FieldChange(nil), plan.Actions[0].Changes...)
	changed.Actions[0].Changes[0].After = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	_, err = ResumeResetPlan(cp, changed)
	require.ErrorContains(t, err, "stale reset resume")
}

func TestResumeResetPlanRejectsCheckpointFingerprintMismatch(t *testing.T) {
	g, st := resetTestGraph(t)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	digest, err := planActionsSHA256(plan.Actions)
	require.NoError(t, err)
	cp := &OperationCheckpoint{Plan: append([]model.PlannedChange(nil), plan.Actions...), PlanSHA256: digest}
	cp.Plan[0].Reason = "tampered"

	_, err = ResumeResetPlan(cp, plan)
	require.ErrorContains(t, err, "checkpoint plan fingerprint")
}

func TestResumeResetPlanRejectsInvalidPlanBeforeSkippingCompletedSteps(t *testing.T) {
	for _, scenario := range []string{"duplicate-current", "completed-create", "duplicate-completed"} {
		t.Run(scenario, func(t *testing.T) {
			g, st := resetTestGraph(t)
			current, err := BuildResetPlan(g, st, "")
			require.NoError(t, err)
			cp := &OperationCheckpoint{Plan: append([]model.PlannedChange(nil), current.Actions...)}
			switch scenario {
			case "duplicate-current":
				current.Actions = append(current.Actions, current.Actions[0])
			case "completed-create":
				current.Actions[0].Action = model.PlanActionCreate
				cp.Plan[0].Action = model.PlanActionCreate
			case "duplicate-completed":
				cp.Plan = append(cp.Plan, cp.Plan[0])
			}
			cp.Steps = []OperationStep{{Kind: "resource", Resource: cp.Plan[0].Address.String(), Action: model.PlanActionReset, Status: OperationDone}}
			cp.PlanSHA256, err = planActionsSHA256(cp.Plan)
			require.NoError(t, err)
			_, err = ResumeResetPlan(cp, current)
			require.Error(t, err)
		})
	}
}

func TestExecutorResetResumesPersistedProviderHandleWithoutPreparingAgain(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-resume", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	require.NoError(t, node.SetRuntimeValue("reset_handle", `{"old_id":"old-first"}`))
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)

	require.NoError(t, testActionExecutor(t, g, st).Reset(context.Background(), plan))
	require.Equal(t, []string{"observe", "destroy", "apply", "observe", "cleanup"}, sub.resetLifecycle)
}

type failSecondResetIntent struct {
	*memoryActions
	calls int
}

type resetOutcomeContract struct{ *memoryActions }

type rejectResetDestroyClaim struct{ *memoryActions }

type rejectDestroyedCommit struct{ *memoryActions }

type rejectResetReplacement struct{ *memoryActions }

func (m rejectResetReplacement) CommitActionOutcome(ctx context.Context, r ActionRecord, p *StatePatch) error {
	if r.Replacement != nil {
		return errors.New("replacement publication unavailable")
	}
	return m.memoryActions.CommitActionOutcome(ctx, r, p)
}

func TestResetPublishesReplacementBeforeGuestInitialization(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	g.Get(address.Resource("sysbox_node", "first")).Data.(*config.NodeConfig).Routes = []config.RouteConfig{{Destination: "10.90.0.0/24", Via: "10.0.0.1"}}
	e := testActionExecutor(t, g, st)
	m := e.actions.(*memoryActions)
	e.SetActionRepository(rejectResetReplacement{m})
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.ErrorContains(t, e.Reset(context.Background(), plan), "replacement publication unavailable")
	require.Equal(t, []string{"prepare:old-first", "destroy", "apply"}, sub.resetLifecycle, "guest initialization/final verification cannot follow failed target publication")
	require.Zero(t, sub.routeChecks, "route guest commands must wait for replacement publication")
	require.Equal(t, "applying", m.actions[0].Phase)
	require.False(t, m.actions[0].EffectComplete)
	require.Nil(t, m.actions[0].Replacement)
	require.Equal(t, int64(1), m.actions[0].Candidate.Generation)
}

func (m rejectDestroyedCommit) CommitActionOutcome(ctx context.Context, r ActionRecord, p *StatePatch) error {
	if r.Phase == "destroyed" {
		return errors.New("destroyed commit unavailable")
	}
	return m.memoryActions.CommitActionOutcome(ctx, r, p)
}

func TestResetReturnedDestroySurvivesObservationCommitFailure(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	m := e.actions.(*memoryActions)
	e.SetActionRepository(rejectDestroyedCommit{m})
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.ErrorContains(t, e.Reset(context.Background(), plan), "destroyed commit unavailable")
	require.Equal(t, "destroy_returned", m.actions[0].Phase)
	require.Equal(t, []string{"prepare:old-first", "destroy"}, sub.resetLifecycle)
	coldState, err := m.LoadState(context.Background(), e.topology)
	require.NoError(t, err)
	cold := NewExecutor(g, coldState, driver.DefaultRegistry)
	cold.SetRunContext(e.topology, "cold")
	cold.SetResumeRunID(e.runID)
	cold.SetActionRepository(resetOutcomeContract{m})
	require.NoError(t, cold.Reset(context.Background(), plan))
	require.Equal(t, ActionSucceeded, m.actions[0].Status)
	count := 0
	for _, event := range sub.resetLifecycle {
		if event == "destroy" {
			count++
		}
	}
	require.Equal(t, 1, count, "cold recovery may observe but must not destroy again")
}

func (m rejectResetDestroyClaim) CommitActionOutcome(ctx context.Context, r ActionRecord, p *StatePatch) error {
	if r.Phase == "destroying" {
		return errors.New("destroy claim rejected")
	}
	return m.memoryActions.CommitActionOutcome(ctx, r, p)
}

func TestResetDestroyRequiresCommittedClaim(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	m := e.actions.(*memoryActions)
	e.SetActionRepository(rejectResetDestroyClaim{m})
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.ErrorContains(t, e.Reset(context.Background(), plan), "destroy claim rejected")
	require.Equal(t, []string{"prepare:old-first"}, sub.resetLifecycle)
	require.Equal(t, "prepared", m.actions[0].Phase)
	// A committed claim without a returned callback is not permission to replay.
	m.actions[0].Phase = "destroying"
	cold := NewExecutor(g, st, driver.DefaultRegistry)
	cold.SetRunContext(e.topology, "cold")
	cold.SetResumeRunID(e.runID)
	cold.SetActionRepository(m)
	require.ErrorContains(t, cold.Reset(context.Background(), plan), "destruction outcome unknown")
	require.Equal(t, []string{"prepare:old-first"}, sub.resetLifecycle)
}

func (m resetOutcomeContract) CommitActionOutcome(ctx context.Context, r ActionRecord, p *StatePatch) error {
	if r.Status != ActionSucceeded && r.Status != ActionUncertain && r.Status != ActionBlocked {
		return errors.New("invalid outcome status")
	}
	return m.memoryActions.CommitActionOutcome(ctx, r, p)
}

func TestResetNewRunCannotTakeOverPendingActions(t *testing.T) {
	for _, resume := range []string{"", "unrelated-run"} {
		t.Run("resume="+resume, func(t *testing.T) {
			sub := &portTestSubstrate{name: "fake", coldPlug: true}
			registerPortTestDriver(t, sub)
			g, st := configuredResetTestState(t, sub, "first")
			e := testActionExecutor(t, g, st)
			m := e.actions.(*memoryActions)
			m.outcomeErr = errors.New("crash before prepare")
			plan, err := BuildResetPlan(g, st, "sysbox_node.first")
			require.NoError(t, err)
			require.Error(t, e.Reset(context.Background(), plan))
			before := append([]ActionRecord(nil), m.actions...)
			m.outcomeErr = nil
			cold := NewExecutor(g, st, driver.DefaultRegistry)
			cold.SetRunContext(e.topology, "new-run")
			cold.SetResumeRunID(resume)
			cold.SetActionRepository(m)
			require.ErrorIs(t, cold.Reset(context.Background(), plan), ErrPlanStale)
			require.Empty(t, sub.resetLifecycle)
			require.Equal(t, before, m.actions)
		})
	}
}

func TestResetAcceptedIntentColdRecoveryBeforePrepare(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	m := e.actions.(*memoryActions)
	e.SetActionRepository(resetOutcomeContract{m})
	m.outcomeErr = errors.New("prepare marker write failed")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.ErrorContains(t, e.Reset(context.Background(), plan), "prepare marker write failed")
	require.Empty(t, sub.resetLifecycle)
	require.Len(t, m.actions, 1)
	require.Equal(t, "accepted", m.actions[0].Phase)
	id := m.actions[0].ID
	m.outcomeErr = nil
	// Once the prepare marker committed, a crash may have followed an effect.
	// That phase must never be treated as the safe, not-yet-started phase.
	m.actions[0].Phase = "preparing"
	uncertain := NewExecutor(g, st, driver.DefaultRegistry)
	uncertain.SetRunContext(e.topology, "uncertain-reset")
	uncertain.SetResumeRunID(e.runID)
	uncertain.SetActionRepository(m)
	require.ErrorContains(t, uncertain.Reset(context.Background(), plan), "preparation outcome unknown")
	require.Empty(t, sub.resetLifecycle)
	m.actions[0].Phase = "accepted"
	coldState, err := m.LoadState(context.Background(), e.topology)
	require.NoError(t, err)
	cold := NewExecutor(g, coldState, driver.DefaultRegistry)
	cold.SetRunContext(e.topology, "cold-reset")
	cold.SetResumeRunID(e.runID)
	cold.SetActionRepository(resetOutcomeContract{m})
	require.NoError(t, cold.Reset(context.Background(), plan))
	require.Len(t, m.actions, 1)
	require.Equal(t, id, m.actions[0].ID)
	require.Equal(t, ActionSucceeded, m.actions[0].Status)
	require.Equal(t, []string{"prepare:old-first", "destroy", "apply", "observe", "cleanup"}, sub.resetLifecycle)
}

func (m *failSecondResetIntent) CommitResetIntents(ctx context.Context, batch []ActionRecord) error {
	staged := *m.memoryActions
	staged.actions = append([]ActionRecord(nil), staged.actions...)
	for _, r := range batch {
		m.calls++
		if m.calls == 2 {
			return errors.New("second reset intent unavailable")
		}
		if err := staged.CommitActionIntent(ctx, r); err != nil {
			return err
		}
	}
	m.actions = staged.actions
	return nil
}

func TestResetPersistsWholeIntentSetBeforeProviderEffects(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	node := st.FindResource(address.Resource("sysbox_node", "second"))
	node.Driver = sub.name
	node.Attributes = state.MustAttributes(map[string]any{"guest_family": "linux"})
	require.NoError(t, node.SetRuntimeValue("container_id", "old-second"))
	e := testActionExecutor(t, g, st)
	m := &failSecondResetIntent{memoryActions: e.actions.(*memoryActions)}
	e.SetActionRepository(m)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	require.ErrorContains(t, e.Reset(context.Background(), plan), "second reset intent unavailable")
	require.Equal(t, 2, m.calls)
	require.Empty(t, m.actions, "failed batch must not publish its first intent")
	require.Empty(t, sub.resetLifecycle, "failed intent publication must precede prepare/destroy/apply")
}

func TestResetBatchColdRecoveryRetainsBothOriginalActions(t *testing.T) {
	sub := &portTestSubstrate{name: "fake", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	second := st.FindResource(address.Resource("sysbox_node", "second"))
	second.Driver = sub.name
	second.Attributes = state.MustAttributes(map[string]any{"guest_family": "linux"})
	require.NoError(t, second.SetRuntimeValue("container_id", "old-second"))
	e := testActionExecutor(t, g, st)
	m := e.actions.(*memoryActions)
	m.outcomeErr = errors.New("crash before prepare marker")
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	require.ErrorContains(t, e.Reset(context.Background(), plan), "crash before prepare marker")
	require.Len(t, m.actions, 2)
	require.Empty(t, sub.resetLifecycle)
	before := append([]ActionRecord(nil), m.actions...)
	m.outcomeErr = nil
	coldState, err := m.LoadState(context.Background(), e.topology)
	require.NoError(t, err)
	cold := NewExecutor(g, coldState, driver.DefaultRegistry)
	cold.SetRunContext(e.topology, "new-process")
	cold.SetResumeRunID(e.runID)
	cold.SetActionRepository(resetOutcomeContract{m})
	require.NoError(t, cold.Reset(context.Background(), plan))
	require.Len(t, m.actions, 2)
	for i, a := range m.actions {
		require.Equal(t, before[i].ID, a.ID)
		require.Equal(t, before[i].RunID, a.RunID)
		require.Equal(t, before[i].Generation, a.Generation)
		require.Equal(t, ActionSucceeded, a.Status)
	}
}

func TestExecutorResetRejectsOwnedResidue(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-residue", coldPlug: true, resetObservation: substrate.ResetObservation{
		Phase: substrate.ResetPhaseComplete, Converged: true, Residue: []string{"old-container"},
	}}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)

	exec := testActionExecutor(t, g, st)
	err = exec.Reset(context.Background(), plan)
	require.ErrorContains(t, err, "left owned residue")
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	require.Equal(t, "old-first", node.ContainerID())
	resetHandle, _ := exec.actions.(*memoryActions).actions[0].Candidate.RuntimeValue("reset_handle").(string)
	require.NotEmpty(t, resetHandle)
}

func TestExecutorResetPersistsRecoveryHandleBeforeApplyMutation(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-checkpoint", coldPlug: true, resetApplyErr: errors.New("injected apply failure")}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	dir := t.TempDir()
	mgr := state.NewManager(filepath.Join(dir, "state.json"))
	require.NoError(t, mgr.Save(st))
	exec := testActionExecutor(t, g, st)
	exec.SetRecorder(NewFileRecorder(filepath.Join(dir, "reset.checkpoint.json"), "run-reset", "lab"))
	exec.SetStatePatchSink(&StatePatchManagerSink{Manager: mgr, State: st})

	err = exec.Reset(context.Background(), plan)
	require.ErrorContains(t, err, "injected apply failure")
	persisted, err := exec.actions.LoadState(context.Background(), exec.topology)
	require.NoError(t, err)
	require.Equal(t, "old-first", persisted.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
	resetHandle, _ := exec.actions.(*memoryActions).actions[0].Candidate.RuntimeValue("reset_handle").(string)
	require.NotEmpty(t, resetHandle)
	cp, err := LoadCheckpointFile(filepath.Join(dir, "reset.checkpoint.json"))
	require.NoError(t, err)
	require.NotEmpty(t, cp.PlanSHA256)
	requireResetCheckpointPhase(t, cp, "prepare_reset", OperationDone)
	requireResetCheckpointPhase(t, cp, "apply_reset", OperationFailed)
}

type failingResetPatchSink struct{}

func (failingResetPatchSink) ApplyStatePatch(context.Context, StatePatch) error {
	return errors.New("injected state patch failure")
}

type failFinalResetPatchSink struct {
	inner StatePatchSink
	calls int
}

func (s *failFinalResetPatchSink) ApplyStatePatch(ctx context.Context, patch StatePatch) error {
	s.calls++
	if s.calls == 2 {
		return errors.New("injected final state patch failure")
	}
	return s.inner.ApplyStatePatch(ctx, patch)
}

func TestExecutorResetRollsBackCompletedStateWhenFinalPatchFails(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-final-patch-failure", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, mgr.Save(st))
	exec := testActionExecutor(t, g, st)
	exec.actions.(*memoryActions).patchErr = errors.New("injected final state patch failure")

	err = exec.Reset(context.Background(), plan)
	require.ErrorContains(t, err, "injected final state patch failure")
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	require.Equal(t, "old-first", node.ContainerID())
	require.Equal(t, "node-reset", exec.actions.(*memoryActions).actions[0].Candidate.ContainerID())
	require.NotEqual(t, ActionSucceeded, exec.actions.(*memoryActions).actions[0].Status)
}

func TestExecutorResetPersistsCompletedStateThroughManagerPatchSink(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-manager-patch", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, mgr.Save(st))
	exec := testActionExecutor(t, g, st)
	exec.SetStatePatchSink(&StatePatchManagerSink{Manager: mgr, State: st})

	require.NoError(t, exec.Reset(context.Background(), plan))
	require.Equal(t, "node-reset", st.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
	persisted, err := exec.actions.LoadState(context.Background(), exec.topology)
	require.NoError(t, err)
	require.Equal(t, "node-reset", persisted.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
}

func TestExecutorResetDoesNotCompleteResourceWhenStatePatchFails(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-patch-failure", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	dir := t.TempDir()
	checkpointPath := filepath.Join(dir, "reset.checkpoint.json")
	exec := testActionExecutor(t, g, st)
	exec.SetRecorder(NewFileRecorder(checkpointPath, "run-reset", "lab"))
	exec.actions.(*memoryActions).patchErr = errors.New("injected state patch failure")

	err = exec.Reset(context.Background(), plan)
	require.ErrorContains(t, err, "injected state patch failure")
	cp, err := LoadCheckpointFile(checkpointPath)
	require.NoError(t, err)
	require.Equal(t, OperationFailed, cp.Steps[0].Status)
}

func requireResetCheckpointPhase(t *testing.T, cp *OperationCheckpoint, phase string, status OperationStatus) {
	t.Helper()
	for _, step := range cp.Steps {
		if step.Phase == phase {
			require.Equal(t, status, step.Status)
			return
		}
	}
	require.Failf(t, "missing reset checkpoint phase", "phase %q was not recorded", phase)
}

func configuredResetTestState(t *testing.T, sub *portTestSubstrate, nodeName string) (*graph.Graph, *state.State) {
	t.Helper()
	g, st := resetTestGraph(t)
	image := st.FindResource(address.Resource("sysbox_image", "base"))
	image.Driver = sub.name
	image.Attributes = state.MustAttributes(map[string]any{
		"image_id": "base-id", "kind": "rootfs", "source": "/base",
		"sha256":       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"architecture": "amd64", "guest_family": "linux",
	})
	node := st.FindResource(address.Resource("sysbox_node", nodeName))
	node.Driver = sub.name
	node.Attributes = state.MustAttributes(map[string]any{"guest_family": "linux"})
	require.NoError(t, node.SetRuntimeValue("container_id", "old-"+nodeName))
	return g, st
}

func TestResetRecoveryDoesNotAdvanceGenerationOrReplayEffects(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-recover-commit", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	repo := e.actions.(*memoryActions)
	repo.patchErr = errors.New("commit lost")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.Error(t, e.Reset(context.Background(), plan))
	require.Len(t, repo.actions, 1)
	require.Equal(t, int64(2), repo.actions[0].Generation)
	before := append([]string(nil), sub.resetLifecycle...)
	repo.patchErr = nil
	restarted := NewExecutor(g, st, driver.DefaultRegistry)
	restarted.SetRunContext(e.topology, "run-2")
	restarted.SetResumeRunID(e.runID)
	restarted.SetActionRepository(repo)
	require.NoError(t, restarted.Reset(context.Background(), plan))
	require.Equal(t, before, sub.resetLifecycle)
	require.Equal(t, int64(2), st.FindResource(address.Resource("sysbox_node", "first")).Generation)
}
func TestResetUnknownObservationPreventsIntentAndEffects(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-unknown", nodeObservation: substrate.NodeObservation{Status: substrate.NodeStatusUnknown}}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.ErrorIs(t, e.Reset(context.Background(), plan), ErrObservationUnknown)
	require.Empty(t, sub.resetLifecycle)
	require.Empty(t, e.actions.(*memoryActions).actions)
}
func TestResetInterruptedGuestSetupIsNotReplayed(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-interrupted", resetApplyErr: errors.New("response lost")}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.Error(t, e.Reset(context.Background(), plan))
	before := append([]string(nil), sub.resetLifecycle...)
	sub.resetApplyErr = nil
	require.ErrorIs(t, e.Reset(context.Background(), plan), ErrActionUncertain)
	require.Equal(t, before, sub.resetLifecycle)
}
func TestResetRecoveryRejectsChangedBaseline(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-baseline-change"}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	e := testActionExecutor(t, g, st)
	repo := e.actions.(*memoryActions)
	repo.patchErr = errors.New("commit lost")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.Error(t, e.Reset(context.Background(), plan))
	repo.patchErr = nil
	image := st.FindResource(address.Resource("sysbox_image", "base"))
	require.NoError(t, image.SetAttribute("sha256", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	changed, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	require.ErrorIs(t, e.Reset(context.Background(), changed), ErrPlanStale)
}

func TestNewResetSkipsDisposedAdoptionButOldRequestCannotResume(t *testing.T) {
	for _, oldRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(oldRequest), func(t *testing.T) {
			sub := &portTestSubstrate{name: "fake", coldPlug: true}
			registerPortTestDriver(t, sub)
			g, st := configuredResetTestState(t, sub, "first")
			e := testActionExecutor(t, g, st)
			e.runID = "new-run"
			old := ActionRecord{ID: "disposed-adoption", RunID: "old-adoption", Action: model.PlanActionAdopt, Status: ActionUncertain, DispositionID: "fence-receipt", Resource: address.Resource("sysbox_node", "first")}
			m := e.actions.(*memoryActions)
			m.actions = append(m.actions, old)
			if oldRequest {
				e.runID = old.RunID
			}
			plan, err := BuildResetPlan(g, st, "sysbox_node.first")
			require.NoError(t, err)
			err = e.Reset(context.Background(), plan)
			if oldRequest {
				require.Error(t, err)
				require.Empty(t, sub.resetLifecycle)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, sub.resetLifecycle)
			}
			require.Equal(t, old, m.actions[0], "history remains unknown")
		})
	}
}
