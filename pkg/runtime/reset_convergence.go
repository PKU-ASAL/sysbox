package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type resetAction struct {
	record   ActionRecord
	node     *resetNodeContext
	complete bool
}

func (e *Executor) convergeReset(ctx context.Context, plan *Plan) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan == nil {
		return fmt.Errorf("missing reset plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	// Decode and validate every pinned baseline before any provider effect.
	for _, change := range plan.Actions {
		if change.Action != controlplane.PlanActionReset {
			return fmt.Errorf("reset plan contains non-reset action %s", change.Action)
		}
		if _, err := e.buildResetNodeContext(ctx, change); err != nil {
			return err
		}
	}
	if e.actions == nil {
		return fmt.Errorf("%w: reset requires durable action repository", ErrConvergenceUnsupported)
	}
	durable, err := e.actions.LoadState(ctx, e.topology)
	if err != nil {
		return err
	}
	if durable.Meta.Serial != e.state.Meta.Serial {
		return fmt.Errorf("%w: state serial changed", ErrPlanStale)
	}
	if e.state.TopologyUID != "" && e.state.TopologyUID != durable.TopologyUID {
		return fmt.Errorf("%w: topology ownership changed", ErrPlanStale)
	}
	e.state.TopologyUID = durable.TopologyUID
	prior, err := e.actions.LoadActions(ctx, e.topology)
	if err != nil {
		return err
	}
	pending := map[string]ActionRecord{}
	permitted := map[string]bool{}
	for _, change := range plan.Actions {
		permitted[change.Address.String()] = true
	}
	for _, record := range prior {
		if record.Status == ActionSucceeded {
			if record.Action != controlplane.PlanActionReset || record.RunID != e.resumeRunID || !permitted[record.Resource.String()] {
				continue
			}
			if record.Candidate == nil {
				return fmt.Errorf("%w: completed reset missing identity", ErrActionUncertain)
			}
			handler, _ := GetResourceHandler(record.Resource.Type)
			observed, err := checkedRead(ctx, handler, *record.Candidate, e.state)
			if err != nil {
				return err
			}
			if observed.Status != state.ResourcePresent {
				return fmt.Errorf("%w: completed reset drifted", ErrPlanStale)
			}
			pending[record.Resource.String()] = record
			continue
		}
		if record.Action != controlplane.PlanActionReset || !permitted[record.Resource.String()] {
			return fmt.Errorf("%w: unresolved action %s outside reset scope", ErrPlanStale, record.ID)
		}
		if record.RunID != e.runID && (e.resumeRunID == "" || record.RunID != e.resumeRunID) {
			return fmt.Errorf("%w: reset action %s belongs to Run %s; explicit continuation is required", ErrPlanStale, record.ID, record.RunID)
		}
		pending[record.Resource.String()] = record
	}
	staged, err := cloneExecutionState(e.state)
	if err != nil {
		return err
	}
	worker := *e
	worker.state = staged
	worker.patchSink = nil
	entries := make([]*resetAction, 0, len(plan.Actions))
	for _, change := range plan.Actions {
		record, resuming := pending[change.Address.String()]
		expectedDigest, digestErr := desiredActionDigest(e.graph.Get(change.Address), change)
		if digestErr != nil {
			return digestErr
		}
		if resuming && expectedDigest != record.DesiredDigest {
			return fmt.Errorf("%w: reset target or baseline changed", ErrPlanStale)
		}
		if resuming && record.Status == ActionSucceeded {
			continue
		}
		if resuming {
			digest, err := desiredActionDigest(e.graph.Get(change.Address), change)
			if err != nil {
				return err
			}
			if digest != record.DesiredDigest {
				return fmt.Errorf("%w: reset target changed", ErrPlanStale)
			}
			if record.Candidate == nil {
				return fmt.Errorf("%w: reset %s has no recoverable identity", ErrActionUncertain, record.ID)
			}
			if record.OwnerUID != e.state.TopologyUID {
				return fmt.Errorf("%w: reset owner mismatch", ErrObservationUnknown)
			}
			if record.Phase == "applying" && !record.EffectComplete {
				return fmt.Errorf("%w: reset %s interrupted during guest setup; refusing replay", ErrActionUncertain, record.ID)
			}
			if record.Phase == "" || record.Phase == "preparing" {
				return fmt.Errorf("%w: reset preparation outcome unknown", ErrActionUncertain)
			}
			if record.Phase == "destroying" {
				return fmt.Errorf("%w: reset destruction outcome unknown", ErrActionUncertain)
			}
			restoreResetStateResource(staged, *record.Candidate)
		} else {
			observed, err := e.observeTarget(ctx, change.Address)
			if err != nil {
				return err
			}
			if observed.Status == state.ResourceAbsent {
				return fmt.Errorf("%w: reset source absent", ErrPlanStale)
			}
			record, err = e.newAction(ctx, change)
			if err != nil {
				return err
			}
		}
		item, err := worker.buildResetNodeContext(ctx, change)
		if err != nil {
			return err
		}
		entries = append(entries, &resetAction{record: record, node: item, complete: record.EffectComplete})
	}
	if err := e.recorder.Begin("reset", plan); err != nil {
		return err
	}
	defer func() { resultErr = e.finishJournal(resultErr) }()
	// Publish every new intent before any provider preparation or destruction.
	// A later storage failure must not leave an earlier VM already destroyed.
	var admission []ActionRecord
	for i := len(entries) - 1; i >= 0; i-- {
		record := &entries[i].record
		if _, resuming := pending[record.Resource.String()]; resuming {
			continue
		}
		record.Phase = "accepted"
		admission = append(admission, *record)
	}
	if len(admission) != 0 {
		batch, ok := e.actions.(ResetIntentCommitter)
		if !ok {
			return fmt.Errorf("%w: reset requires atomic intent admission", ErrConvergenceUnsupported)
		}
		if err := batch.CommitResetIntents(ctx, admission); err != nil {
			return err
		}
		for _, entry := range entries {
			if _, resuming := pending[entry.record.Resource.String()]; !resuming {
				entry.record.Revision++
			}
		}
	}
	// Prepare/delete in reverse dependency order, then instantiate forward.
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		record := &entry.record
		item := entry.node
		item.step = e.recorder.StepStart(record.Resource.String(), controlplane.PlanActionReset)
		if err := e.journalError(); err != nil {
			return err
		}
		worker.activeReset = record
		actionCtx := substrate.WithOperationIdentity(ctx, actionIdentity(*record))
		if entry.complete {
			continue
		}
		if record.Phase == "accepted" {
			record.Phase = "preparing"
			record.Status = ActionUncertain
			if err := e.actions.CommitActionOutcome(actionCtx, *record, nil); err != nil {
				return err
			}
			record.Revision++
			if err := worker.prepareResetNode(actionCtx, item); err != nil {
				return e.uncertainAction(ctx, *record, item.step, err)
			}
			record.Phase = "prepared"
			if err := worker.recordStepExternal(actionCtx, item.step, item.action.Address, controlplane.PlanActionReset); err != nil {
				return err
			}
		} else {
			raw, _ := item.current.RuntimeValue("reset_handle").(string)
			if raw == "" {
				return fmt.Errorf("%w: reset handle missing", ErrActionUncertain)
			}
			handle, err := item.resetDriver.UnmarshalResetHandle(json.RawMessage(raw))
			if err != nil {
				return err
			}
			item.handle = handle
			item.handle.Request = item.request
			observation, err := item.resetDriver.ObserveReset(actionCtx, item.handle)
			if err != nil {
				return err
			}
			if observation.BaselineDigest != "" && observation.BaselineDigest != item.request.Baseline.Digest {
				return fmt.Errorf("%w: reset baseline changed", ErrPlanStale)
			}
		}
		handler, _ := GetResourceHandler(item.action.Address.Type)
		old, err := checkedRead(actionCtx, handler, *item.current, e.state)
		if err != nil {
			return err
		}
		if record.Phase == "prepared" {
			record.Phase = "destroying"
			record.Status = ActionUncertain
			if err := e.actions.CommitActionOutcome(actionCtx, *record, nil); err != nil {
				return err
			}
			record.Revision++
		}
		if old.Status != state.ResourceAbsent {
			if record.Phase != "destroying" {
				return fmt.Errorf("%w: old reset generation reappeared", ErrObservationUnknown)
			}
			if err := worker.destroyResetNode(actionCtx, item); err != nil {
				return e.uncertainAction(ctx, *record, item.step, err)
			}
		}
		if record.Phase == "destroying" {
			// Persist callback return separately from the subsequent absence
			// observation. Recovery may observe, but cannot repeat destruction.
			record.Phase = "destroy_returned"
			if err := worker.recordStepExternal(actionCtx, item.step, item.action.Address, controlplane.PlanActionReset); err != nil {
				return err
			}
		}
		// The old generation must be gone before the replacement is authorized.
		old, err = checkedRead(actionCtx, handler, *item.current, e.state)
		if err != nil {
			return e.uncertainAction(ctx, *record, item.step, err)
		}
		if old.Status != state.ResourceAbsent {
			return e.uncertainAction(ctx, *record, item.step, fmt.Errorf("%w: reset old generation still present", ErrVerification))
		}
		record.Phase = "destroyed"
		if err := worker.recordStepExternal(actionCtx, item.step, item.action.Address, controlplane.PlanActionReset); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		record := &entry.record
		item := entry.node
		worker.activeReset = record
		worker.state.Meta = e.state.Meta
		record.ExpectedSerial = e.state.Meta.Serial
		actionCtx := substrate.WithOperationIdentity(ctx, actionIdentity(*record))
		if !entry.complete {
			record.Phase = "applying"
			if err := e.actions.CommitActionOutcome(actionCtx, *record, nil); err != nil {
				return err
			}
			record.Revision++
			if err := worker.applyResetNode(actionCtx, item); err != nil {
				return e.uncertainAction(ctx, *record, item.step, err)
			}
			item.current.OwnerUID = record.OwnerUID
			item.current.ResourceUID = record.ResourceUID
			item.current.Generation = record.Generation
			record.Phase = "verifying"
			record.EffectComplete = true
			if err := worker.recordStepExternal(actionCtx, item.step, item.action.Address, controlplane.PlanActionReset); err != nil {
				return err
			}
		}
		handler, _ := GetResourceHandler(record.Resource.Type)
		observed, err := checkedRead(actionCtx, handler, *record.Candidate, e.state)
		if err != nil {
			return e.uncertainAction(ctx, *record, item.step, err)
		}
		if observed.Status != state.ResourcePresent {
			return e.uncertainAction(ctx, *record, item.step, ErrVerification)
		}
		record.ExpectedSerial = e.state.Meta.Serial
		if err := e.commitVerifiedAction(actionCtx, *record, item.step); err != nil {
			return err
		}
	}
	return nil
}
