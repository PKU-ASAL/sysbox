package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (e *Executor) Destroy(ctx context.Context, plan *Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate plan: %w", err)
	}
	if err := e.recorder.Begin("destroy", plan); err != nil {
		return err
	}
	var destroyErr error
	defer func() { e.recorder.Finish(destroyErr) }()
	for _, change := range plan.Actions {
		if change.Action == controlplane.PlanActionUnknown {
			destroyErr = fmt.Errorf("cannot destroy %s: %s", change.Address, change.Reason)
			return destroyErr
		}
		if change.Action != controlplane.PlanActionDelete {
			continue
		}
		if err := ctx.Err(); err != nil {
			destroyErr = err
			return err
		}
		current := e.state.FindResource(change.Address)
		if current == nil {
			continue
		}
		e.logf("[destroy] removing %s\n", change.Address)
		step := e.recorder.StepStart(change.Address.String(), change.Action)
		if err := e.DestroyResource(ctx, *current); err != nil {
			e.recorder.StepFailed(step, err)
			if destroyErr == nil {
				destroyErr = fmt.Errorf("destroy %s: %w", change.Address, err)
			}
			continue
		}
		e.recordDeletePatch(ctx, step, *current, change.Action)
		e.recorder.StepDone(step)
	}
	return destroyErr
}
