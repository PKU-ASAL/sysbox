package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type JournalReplayReport struct {
	Applied []RecoverAction `json:"applied,omitempty"`
	Skipped []RecoverAction `json:"skipped,omitempty"`
}

// Legacy checkpoints are diagnostic projections. They cannot authorize state
// writes or provider effects in the canonical lifecycle repository.
func replayCheckpointJournal(context.Context, apiStore, string, string, *state.Manager, string) (*JournalReplayReport, error) {
	return nil, fmt.Errorf("%w: checkpoint patches are not recovery authority; durable action intents are required", runtime.ErrConvergenceUnsupported)
}
func reconcileCheckpointJournal(context.Context, apiStore, string, string, *state.Manager, string) (*RecoverReport, error) {
	return nil, fmt.Errorf("%w: checkpoint-only recovery is disabled; use canonical action recovery", runtime.ErrConvergenceUnsupported)
}

type recoveryExecution struct {
	run        *controlplane.Run
	executor   *runtime.Executor
	state      *state.State
	graph      *graph.Graph
	repository runtime.ActionRepository
	actions    []runtime.ActionRecord
	plan       *runtime.Plan
}

// loadRecoveryExecution never reads the mutable workspace. The original
// content-addressed revision and durable action identities define its scope.
func (s *Server) loadRecoveryExecution(ctx context.Context, topology, runID string, mgr *state.Manager) (*recoveryExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, err := s.apiStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Topology != topology {
		return nil, fmt.Errorf("run topology mismatch")
	}
	if run.Status.IsActive() {
		return nil, fmt.Errorf("run %s is still active", runID)
	}
	if run.AgentID != "" {
		return nil, fmt.Errorf("recovery must execute on owning agent %s; resume the pinned run on that agent", run.AgentID)
	}
	if run.Revision == "" {
		return nil, fmt.Errorf("%w: recovery requires the run's pinned immutable revision", runtime.ErrConvergenceUnsupported)
	}
	if err := mgr.CheckMutationSafety(); err != nil {
		return nil, err
	}
	// Refuse an already-frozen topology before materializing revisions or
	// invoking provider observers/cleanup. This preflight is not a substitute
	// for transaction-level fencing of a freeze starting after this check.
	current, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return nil, err
	}
	authority, err := (apiExecutionGrantStorage{store: s.apiStore}).ReadExecutionAuthority(ctx)
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return nil, err
	}
	if err == nil && len(authority) == 0 {
		return nil, persistence.ErrCorruptRecord
	}
	if err := persistence.ValidateMaintenanceWrite(authority, current.TopologyUID); err != nil {
		return nil, err
	}
	revision, err := s.apiStore.GetGlobalRevision(ctx, run.Revision)
	if err != nil {
		return nil, fmt.Errorf("load pinned revision: %w", err)
	}
	if revision.Revision != run.Revision || controlplane.ComputeRevisionDigest(revision.Files) != run.Revision {
		return nil, fmt.Errorf("pinned revision digest mismatch")
	}
	snapshot, err := s.workspaceService().MaterializeRevision(topology, run.Revision, revision.Files)
	if err != nil {
		return nil, fmt.Errorf("verify pinned snapshot: %w", err)
	}
	g, _, st, _, _, err := runtime.LoadWorkspaceWithInputs(snapshot, mgr, run.Inputs)
	if err != nil {
		return nil, fmt.Errorf("load pinned recovery inputs: %w", err)
	}
	repository, err := s.actionRepository(topology)
	if err != nil {
		return nil, err
	}
	all, err := repository.LoadActions(ctx, topology)
	if err != nil {
		return nil, err
	}
	actions := []runtime.ActionRecord{}
	chosen := map[string]runtime.ActionRecord{}
	for _, action := range all {
		if action.RunID != runID {
			if action.Status != runtime.ActionSucceeded {
				return nil, fmt.Errorf("%w: another unfinished run owns this topology", runtime.ErrActionUncertain)
			}
			continue
		}
		actions = append(actions, action)
		key := action.Resource.String()
		old, ok := chosen[key]
		if !ok || action.Status != runtime.ActionSucceeded || action.Generation > old.Generation {
			chosen[key] = action
		}
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("%w: run has no durable action intents; checkpoint patches cannot prove completion", runtime.ErrConvergenceUnsupported)
	}
	keys := make([]string, 0, len(chosen))
	for key := range chosen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	plan := &runtime.Plan{}
	for _, key := range keys {
		action := chosen[key]
		change := controlplane.PlannedChange{Address: action.Resource, Action: action.Action, Reason: "recover authorized durable action"}
		if action.Action == controlplane.PlanActionReset {
			reset, err := runtime.BuildResetPlan(g, st, action.Resource.String())
			if err != nil {
				return nil, err
			}
			if len(reset.Actions) != 1 {
				return nil, fmt.Errorf("reset recovery target is ambiguous")
			}
			change = reset.Actions[0]
		}
		if action.Action != controlplane.PlanActionDelete && g.Get(action.Resource) == nil {
			return nil, fmt.Errorf("%w: action target absent from pinned revision", runtime.ErrPlanStale)
		}
		plan.Actions = append(plan.Actions, change)
	}
	executor := runtime.NewExecutor(g, st)
	executor.SetRunContext(topology, runID)
	executor.SetWorkspaceFromHCLFile(snapshot)
	executor.SetActionRepository(repository)
	executor.SetLogger(io.Discard)
	return &recoveryExecution{run: run, executor: executor, state: st, graph: g, repository: repository, actions: actions, plan: plan}, nil
}

func (s *Server) reconcileCanonicalActions(ctx context.Context, topology, runID string, mgr *state.Manager) (*RecoverReport, error) {
	if s.jobs.hasRunning(topology) {
		return nil, fmt.Errorf("topology %s has an active operation", topology)
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	if s.jobs.hasRunning(topology) {
		return nil, fmt.Errorf("topology %s has an active operation", topology)
	}
	execution, err := s.loadRecoveryExecution(ctx, topology, runID, mgr)
	if err != nil {
		return nil, err
	}
	if err := execution.executor.RecoverActions(ctx, execution.plan); err != nil {
		return nil, err
	}
	after, err := execution.repository.LoadActions(ctx, topology)
	if err != nil {
		return nil, err
	}
	completed := map[string]bool{}
	for _, action := range after {
		completed[action.ID] = action.Status == runtime.ActionSucceeded
	}
	report := &RecoverReport{RunID: runID, Topology: topology}
	for _, action := range execution.actions {
		entry := RecoverAction{Resource: action.Resource.String(), Status: "already_recorded"}
		if action.Candidate != nil {
			entry.ExternalID = action.Candidate.ExternalID
		}
		if action.Status != runtime.ActionSucceeded && completed[action.ID] {
			entry.Status = "recovered"
			report.Recovered = append(report.Recovered, entry)
		} else {
			report.Skipped = append(report.Skipped, entry)
		}
	}
	return report, nil
}

func (s *Server) cleanupCanonicalActions(ctx context.Context, topology, runID string) (*CleanupReport, error) {
	if s.jobs.hasRunning(topology) {
		return nil, fmt.Errorf("topology %s has an active operation", topology)
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	if s.jobs.hasRunning(topology) {
		return nil, fmt.Errorf("topology %s has an active operation", topology)
	}
	mgr, err := s.stateManager(topology)
	if err != nil {
		return nil, err
	}
	execution, err := s.loadRecoveryExecution(ctx, topology, runID, mgr)
	if err != nil {
		return nil, err
	}
	// A known complete effect can be restored first. Arbitrary uncertain guest
	// execution remains blocked by the same runner and is never replayed.
	if err := execution.executor.RecoverActions(ctx, execution.plan); err != nil {
		return nil, err
	}
	actions, err := execution.repository.LoadActions(ctx, topology)
	if err != nil {
		return nil, err
	}
	selected := map[string]runtime.ActionRecord{}
	for _, action := range actions {
		if action.RunID != runID || action.Action == controlplane.PlanActionDelete {
			continue
		}
		key := action.Resource.String()
		old, ok := selected[key]
		if !ok || action.Generation > old.Generation {
			selected[key] = action
		}
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	report := &CleanupReport{RunID: runID, Topology: topology}
	plan := &runtime.Plan{}
	removing := []CleanupAction{}
	for _, key := range keys {
		record := selected[key]
		entry := CleanupAction{Resource: key, Status: "not_found"}
		candidate := record.Candidate
		if candidate != nil {
			entry.ExternalID = candidate.ExternalID
			if candidate.Address.Type == "sysbox_network" {
				entry.Class = "network"
			} else if candidate.Driver == "firecracker" {
				entry.Class = "microvm"
			}
		}
		current := execution.state.FindResource(record.Resource)
		if current == nil {
			appendCleanupAction(report, entry)
			continue
		}
		if candidate == nil || current.OwnerUID != record.OwnerUID || current.ResourceUID != record.ResourceUID || current.Generation != record.Generation || current.ExternalID != candidate.ExternalID {
			entry.Status = "identity_conflict"
			entry.Error = "current resource is not the generation created by this run"
			appendCleanupAction(report, entry)
			continue
		}
		plan.Actions = append(plan.Actions, controlplane.PlannedChange{Address: record.Resource, Action: controlplane.PlanActionDelete, Reason: "explicit cleanup of this run's verified generation"})
		entry.Status = "removed"
		removing = append(removing, entry)
	}
	if len(plan.Actions) > 0 {
		if err := execution.executor.Destroy(ctx, plan); err != nil {
			return nil, err
		}
		for _, entry := range removing {
			appendCleanupAction(report, entry)
		}
	}
	return report, nil
}
