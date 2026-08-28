package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func (e *Executor) Apply(ctx context.Context, plan *Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate plan: %w", err)
	}
	operation := e.operation
	if operation == "" {
		operation = "apply"
	}
	if err := e.recorder.Begin(operation, plan); err != nil {
		return err
	}
	var applyErr error
	defer func() { e.recorder.Finish(applyErr) }()

	if err := e.graph.Validate(); err != nil {
		applyErr = fmt.Errorf("graph validation: %w", err)
		return applyErr
	}
	// Track resources created by this apply so a failure can roll them back
	// (destroy in reverse creation order), keeping the environment atomic.
	var created []address.Address
	rollback := func() {
		for i := len(created) - 1; i >= 0; i-- {
			addr := created[i]
			if current := e.state.FindResource(addr); current != nil {
				e.logf("[rollback] removing %s\n", addr)
				if err := e.DestroyResource(ctx, *current); err != nil {
					e.logf("[rollback] destroy %s failed: %v\n", addr, err)
				}
			}
		}
	}
	for _, change := range plan.Actions {
		if err := ctx.Err(); err != nil {
			rollback()
			applyErr = err
			return err
		}
		switch change.Action {
		case controlplane.PlanActionNoop:
			continue
		case controlplane.PlanActionUnknown:
			rollback()
			applyErr = fmt.Errorf("cannot apply unknown action for %s", change.Address)
			return applyErr
		case controlplane.PlanActionReset:
			rollback()
			applyErr = fmt.Errorf("reset action for %s must be executed by the reset operation", change.Address)
			return applyErr
		case controlplane.PlanActionDelete:
			if current := e.state.FindResource(change.Address); current != nil {
				step := e.recorder.StepStart(change.Address.String(), change.Action)
				if err := e.DestroyResource(ctx, *current); err != nil {
					e.recorder.StepFailed(step, err)
					rollback()
					applyErr = fmt.Errorf("delete %s: %w", change.Address, err)
					return applyErr
				}
				e.recordDeletePatch(ctx, step, *current, change.Action)
				e.recorder.StepDone(step)
			}
		case controlplane.PlanActionReplace:
			if current := e.state.FindResource(change.Address); current != nil {
				step := e.recorder.StepStart(change.Address.String(), controlplane.PlanActionDelete)
				if err := e.DestroyResource(ctx, *current); err != nil {
					e.recorder.StepFailed(step, err)
					rollback()
					applyErr = fmt.Errorf("replace delete %s: %w", change.Address, err)
					return applyErr
				}
				e.recordDeletePatch(ctx, step, *current, controlplane.PlanActionDelete)
				e.recorder.StepDone(step)
			}
			if err := e.applyCreate(ctx, change); err != nil {
				rollback()
				applyErr = err
				return err
			}
			created = append(created, change.Address)
		case controlplane.PlanActionCreate, controlplane.PlanActionRead:
			if err := e.applyCreate(ctx, change); err != nil {
				rollback()
				applyErr = err
				return err
			}
			if change.Action == controlplane.PlanActionCreate {
				created = append(created, change.Address)
			}
		}
	}
	return nil
}

func (e *Executor) applyCreate(ctx context.Context, change controlplane.PlannedChange) error {
	e.logf("[apply] %s %s\n", change.Action, change.Address)
	step := e.recorder.StepStart(change.Address.String(), change.Action)
	restoreStep := e.setCurrentResourceStep(step)
	if err := e.recordSubstep(step, "create_resource", map[string]any{"resource": change.Address.String()}, func() error { return e.CreateResource(ctx, change.Address) }); err != nil {
		restoreStep()
		wrapped := fmt.Errorf("%s %s: %w", change.Action, change.Address, err)
		e.recorder.StepFailed(step, wrapped)
		return wrapped
	}
	restoreStep()
	if err := e.recordSubstep(step, "capture_state_resource", map[string]any{"resource": change.Address.String()}, func() error { return e.recordStepExternal(ctx, step, change.Address, change.Action) }); err != nil {
		e.recorder.StepFailed(step, err)
		return err
	}
	e.recorder.StepDone(step)
	return nil
}
