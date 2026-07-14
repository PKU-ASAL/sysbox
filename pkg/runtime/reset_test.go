package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
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

	err = NewExecutor(g, st).Reset(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"prepare:old-second", "prepare:old-first", "apply", "observe", "cleanup", "apply", "observe", "cleanup"}, sub.resetLifecycle)
	require.Equal(t, "node-reset", st.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
	require.Equal(t, "node-reset", st.FindResource(address.Resource("sysbox_node", "second")).ContainerID())
	require.Equal(t, 2, sub.nodeObserveCalls)
}

func TestExecutorResetRejectsUnhealthyFinalObservation(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-unhealthy", coldPlug: true, nodeObservation: substrate.NodeObservation{
		Exists: true, Running: true, Healthy: false, Status: substrate.NodeStatusUnhealthy, Reason: "health check failed",
	}}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)

	err = NewExecutor(g, st).Reset(context.Background(), plan)
	require.ErrorContains(t, err, "final health")
	require.Equal(t, "old-first", st.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
}

func TestBuildResetPlanSupportsWholeTopologyAndExactNodeTarget(t *testing.T) {
	g, st := resetTestGraph(t)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	require.Equal(t, []controlplane.PlannedChange{
		{Address: address.Resource("sysbox_node", "first"), Action: controlplane.PlanActionReset, Reason: "reset mutable guest state from immutable baseline", Changes: []controlplane.FieldChange{{Path: "baseline_digest", After: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{Address: address.Resource("sysbox_node", "second"), Action: controlplane.PlanActionReset, Reason: "reset mutable guest state from immutable baseline", Changes: []controlplane.FieldChange{{Path: "baseline_digest", After: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
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
	require.Equal(t, controlplane.PlanActionReset, plan.Actions[0].Action)
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

	err = NewExecutor(g, st).Reset(context.Background(), plan)
	require.ErrorContains(t, err, "stale reset plan")
	require.Empty(t, sub.resetLifecycle)
}

func TestResumeResetPlanSkipsCompletedNodesAndKeepsFailedNodes(t *testing.T) {
	g, st := resetTestGraph(t)
	plan, err := BuildResetPlan(g, st, "")
	require.NoError(t, err)
	digest, err := planActionsSHA256(plan.Actions)
	require.NoError(t, err)
	cp := &OperationCheckpoint{Plan: append([]controlplane.PlannedChange(nil), plan.Actions...), PlanSHA256: digest, Steps: []OperationStep{
		{Kind: "resource", Resource: "sysbox_node.first", Action: controlplane.PlanActionReset, Status: OperationDone},
		{Kind: "resource", Resource: "sysbox_node.second", Action: controlplane.PlanActionReset, Status: OperationFailed},
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
		{Kind: "resource", Resource: "sysbox_node.first", Action: controlplane.PlanActionReset, Status: OperationDone},
		{Kind: "resource", Resource: "sysbox_node.second", Action: controlplane.PlanActionReset, Status: OperationFailed},
	}}
	pending, err := ResumeResetPlan(first, full)
	require.NoError(t, err)
	pendingDigest, err := planActionsSHA256(pending.Actions)
	require.NoError(t, err)
	second := &OperationCheckpoint{Plan: pending.Actions, PlanSHA256: pendingDigest, Steps: []OperationStep{
		{Kind: "resource", Resource: "sysbox_node.second", Action: controlplane.PlanActionReset, Status: OperationFailed},
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
	cp := &OperationCheckpoint{Plan: append([]controlplane.PlannedChange(nil), plan.Actions...), PlanSHA256: digest}
	changed := &Plan{Actions: append([]controlplane.PlannedChange(nil), plan.Actions...)}
	changed.Actions[0].Changes = append([]controlplane.FieldChange(nil), plan.Actions[0].Changes...)
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
	cp := &OperationCheckpoint{Plan: append([]controlplane.PlannedChange(nil), plan.Actions...), PlanSHA256: digest}
	cp.Plan[0].Reason = "tampered"

	_, err = ResumeResetPlan(cp, plan)
	require.ErrorContains(t, err, "checkpoint plan fingerprint")
}

func TestExecutorResetResumesPersistedProviderHandleWithoutPreparingAgain(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-resume", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	require.NoError(t, node.SetRuntimeValue("reset_handle", `{"old_id":"old-first"}`))
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)

	require.NoError(t, NewExecutor(g, st).Reset(context.Background(), plan))
	require.Equal(t, []string{"observe", "apply", "observe", "cleanup"}, sub.resetLifecycle)
}

func TestExecutorResetRejectsOwnedResidue(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-residue", coldPlug: true, resetObservation: substrate.ResetObservation{
		Phase: substrate.ResetPhaseComplete, Converged: true, Residue: []string{"old-container"},
	}}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)

	err = NewExecutor(g, st).Reset(context.Background(), plan)
	require.ErrorContains(t, err, "left owned residue")
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	require.Equal(t, "old-first", node.ContainerID())
	resetHandle, _ := node.RuntimeValue("reset_handle").(string)
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
	exec := NewExecutor(g, st)
	exec.SetRecorder(NewFileRecorder(filepath.Join(dir, "reset.checkpoint.json"), "run-reset", "lab"))
	exec.SetStatePatchSink(&StatePatchManagerSink{Manager: mgr, State: st})

	err = exec.Reset(context.Background(), plan)
	require.ErrorContains(t, err, "injected apply failure")
	persisted, err := mgr.Load()
	require.NoError(t, err)
	resetHandle, _ := persisted.FindResource(address.Resource("sysbox_node", "first")).RuntimeValue("reset_handle").(string)
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
	exec := NewExecutor(g, st)
	exec.SetStatePatchSink(&failFinalResetPatchSink{inner: &StatePatchManagerSink{Manager: mgr, State: st}})

	err = exec.Reset(context.Background(), plan)
	require.ErrorContains(t, err, "injected final state patch failure")
	node := st.FindResource(address.Resource("sysbox_node", "first"))
	require.Equal(t, "old-first", node.ContainerID())
	resetHandle, _ := node.RuntimeValue("reset_handle").(string)
	require.NotEmpty(t, resetHandle)
}

func TestExecutorResetPersistsCompletedStateThroughManagerPatchSink(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-manager-patch", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, mgr.Save(st))
	exec := NewExecutor(g, st)
	exec.SetStatePatchSink(&StatePatchManagerSink{Manager: mgr, State: st})

	require.NoError(t, exec.Reset(context.Background(), plan))
	require.Equal(t, "node-reset", st.FindResource(address.Resource("sysbox_node", "first")).ContainerID())
	persisted, err := mgr.Load()
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
	exec := NewExecutor(g, st)
	exec.SetRecorder(NewFileRecorder(checkpointPath, "run-reset", "lab"))
	exec.SetStatePatchSink(failingResetPatchSink{})

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
