package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

func (e *Executor) validateResetContinuationPlan(ctx context.Context, cp *OperationCheckpoint) error {
	if e.actions == nil || cp == nil || cp.Operation != "reset" || cp.RunID == "" || cp.RunID == e.runID || e.runID == "" || cp.Topology != e.topology {
		return fmt.Errorf("%w: continuation requires a distinct execution and the original reset checkpoint", ErrPlanStale)
	}
	durable, err := e.actions.LoadState(ctx, e.topology)
	if err != nil {
		return err
	}
	if e.state.TopologyUID == "" || durable.TopologyUID != e.state.TopologyUID || durable.Meta.Serial != e.state.Meta.Serial {
		return fmt.Errorf("%w: continuation state changed", ErrPlanStale)
	}
	current, err := BuildResetPlan(e.graph, e.state, "")
	if err != nil {
		return err
	}
	// Validate the original full scope, but never use checkpoint step statuses
	// to skip work. Only durable action outcomes establish completion.
	_, err = ResumeResetPlan(cp, current)
	return err
}

func (e *Executor) validateContinuationConfig(cp *OperationCheckpoint, members []ActionRecord) error {
	changes := map[string]controlplane.PlannedChange{}
	for _, change := range cp.Plan {
		changes[change.Address.String()] = change
	}
	for _, action := range members {
		change, exists := changes[action.Resource.String()]
		if !exists || action.Action != controlplane.PlanActionReset || action.OwnerUID != e.state.TopologyUID || action.RunID != cp.RunID || action.Topology != e.topology || action.Candidate == nil {
			return fmt.Errorf("%w: continuation action identity mismatch", ErrPlanStale)
		}
		digest, err := desiredActionDigest(e.graph.Get(action.Resource), change)
		if err != nil {
			return err
		}
		if digest != action.DesiredDigest {
			return fmt.Errorf("%w: continuation configuration changed for %s", ErrPlanStale, action.Resource)
		}
	}
	return nil
}

// PrepareResetContinuation constructs a reviewable request without admitting
// intents or invoking provider mutations. The bridge supplies authenticated
// original incarnation fields, holds execution exclusion, and verifies old
// execution quiescence. Persisted receipts retain IDs across controller restarts.
func (e *Executor) PrepareResetContinuation(ctx context.Context, cp *OperationCheckpoint, queuedAt time.Time, revision string) (*ResetContinuationRequest, error) {
	if err := e.validateResetContinuationPlan(ctx, cp); err != nil {
		return nil, err
	}
	committer, ok := e.actions.(ResetContinuationCommitter)
	if !ok {
		return nil, fmt.Errorf("%w: reset continuation admission unavailable", ErrConvergenceUnsupported)
	}
	if queuedAt.IsZero() {
		return nil, fmt.Errorf("%w: original Run incarnation is missing", ErrPlanStale)
	}
	prior, err := e.actions.LoadActions(ctx, e.topology)
	if err != nil {
		return nil, err
	}
	byAddress := map[string]ActionRecord{}
	for _, action := range prior {
		if action.RunID != cp.RunID {
			if action.Status != ActionSucceeded {
				return nil, fmt.Errorf("%w: another Run is unresolved", ErrPlanStale)
			}
			continue
		}
		if !action.RunQueuedAt.Equal(queuedAt) || action.RunRevision != revision {
			return nil, fmt.Errorf("%w: original Run incarnation changed", ErrPlanStale)
		}
		if err := e.validateContinuationConfig(cp, []ActionRecord{action}); err != nil {
			return nil, err
		}
		if !action.ResetContinuationSafe() {
			return nil, fmt.Errorf("%w: reset %s has unknown effect progress", ErrActionUncertain, action.ID)
		}
		if _, exists := byAddress[action.Resource.String()]; exists {
			return nil, fmt.Errorf("%w: duplicate reset action", ErrPlanStale)
		}
		byAddress[action.Resource.String()] = action
	}
	if len(byAddress) == 0 {
		return nil, fmt.Errorf("%w: original reset has no durable actions", ErrPlanStale)
	}
	receipt, err := committer.LoadResetContinuation(ctx, e.topology, cp.RunID)
	if err == nil {
		if receipt == nil || receipt.Request.Checkpoint.PlanSHA256 != cp.PlanSHA256 || receipt.Request.Checkpoint.RunID != cp.RunID || receipt.Request.Checkpoint.Topology != cp.Topology || receipt.Request.TopologyUID != e.state.TopologyUID || !receipt.Request.RunQueuedAt.Equal(queuedAt) || receipt.Request.RunRevision != revision {
			return nil, fmt.Errorf("%w: continuation receipt differs from original Run", ErrPlanStale)
		}
		request := receipt.Request
		return &request, nil
	}
	if !errors.Is(err, ErrResetContinuationNotFound) {
		return nil, err
	}
	req := &ResetContinuationRequest{ID: e.runID, TopologyUID: e.state.TopologyUID, RunQueuedAt: queuedAt, RunRevision: revision, ExpectedSerial: e.state.Meta.Serial, Checkpoint: *cp}
	original := *e
	original.SetRunContext(e.topology, cp.RunID)
	original.SetRunIncarnation(queuedAt, revision)
	for _, change := range cp.Plan {
		if action, exists := byAddress[change.Address.String()]; exists {
			req.Existing = append(req.Existing, action)
			continue
		}
		observed, err := e.observeTarget(ctx, change.Address)
		if err != nil {
			return nil, err
		}
		if observed.Status == state.ResourceAbsent {
			return nil, fmt.Errorf("%w: missing reset source %s", ErrPlanStale, change.Address)
		}
		action, err := original.newAction(ctx, change)
		if err != nil {
			return nil, err
		}
		action.Phase = "accepted"
		req.Missing = append(req.Missing, action)
	}
	return req, nil
}

// ContinueReset admits the approved request before the first provider mutation,
// then executes its full fixed scope using original action identities and the
// current execution's recorder. Caller authorization is required independently.
func (e *Executor) ContinueReset(ctx context.Context, req ResetContinuationRequest) error {
	if err := e.validateResetContinuationPlan(ctx, &req.Checkpoint); err != nil {
		return err
	}
	if err := e.validateContinuationConfig(&req.Checkpoint, req.Existing); err != nil {
		return err
	}
	if err := e.validateContinuationConfig(&req.Checkpoint, req.Missing); err != nil {
		return err
	}
	committer, ok := e.actions.(ResetContinuationCommitter)
	if !ok {
		return fmt.Errorf("%w: reset continuation admission unavailable", ErrConvergenceUnsupported)
	}
	if _, err := committer.CommitResetContinuation(ctx, req); err != nil {
		return err
	}
	worker := *e
	worker.SetResumeRunID(req.Checkpoint.RunID)
	return worker.Reset(ctx, &Plan{Actions: req.Checkpoint.Plan})
}
