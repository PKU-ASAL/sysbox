package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
)

// Apply walks the plan forward: create Add resources and re-create Change
// (drifted) resources, both in topo order.
func (e *Executor) Apply(ctx context.Context, plan *Plan) error {
	if err := ctx.Err(); err != nil {
		return err
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
	order, err := e.graph.TopoSort()
	if err != nil {
		applyErr = err
		return applyErr
	}

	toCreate := map[string]bool{}
	for _, action := range plan.actionsByType(controlplane.PlanActionCreate, controlplane.PlanActionRead, controlplane.PlanActionUpdate, controlplane.PlanActionReplace) {
		toCreate[action.Resource] = true
	}

	// For drifted resources, destroy all affected existing resources in
	// reverse topo order first (dependents before dependencies), then recreate
	// them in normal topo order below.
	changeSet := map[graph.NodeID]bool{}
	for _, action := range plan.actionsByType(controlplane.PlanActionUpdate, controlplane.PlanActionReplace) {
		id := action.NodeID()
		changeSet[id] = true
	}
	if len(changeSet) > 0 {
		reverse, err := e.graph.ReverseTopoSort()
		if err != nil {
			applyErr = err
			return applyErr
		}
		for _, id := range reverse {
			if err := ctx.Err(); err != nil {
				applyErr = err
				return applyErr
			}
			if !changeSet[id] {
				continue
			}
			r := e.state.FindResource(id.Type, id.Name)
			if r != nil {
				e.logf("[apply] removing drifted %s before re-create\n", id)
				step := e.recorder.StepStart(id.String(), controlplane.PlanActionDelete)
				if err := e.DestroyResource(ctx, *r); err != nil {
					e.logf("[apply] warning: cleanup of drifted %s failed: %v\n", id, err)
					e.recorder.StepFailed(step, err)
				} else {
					e.recordDeletePatch(ctx, step, *r, controlplane.PlanActionDelete)
					e.recorder.StepDone(step)
				}
			}
		}
	}

	for _, id := range order {
		if err := ctx.Err(); err != nil {
			applyErr = err
			return applyErr
		}
		if !toCreate[id.String()] {
			continue
		}
		verb := "creating"
		switch actionFor(plan, id) {
		case controlplane.PlanActionReplace, controlplane.PlanActionUpdate:
			verb = "re-creating"
		case controlplane.PlanActionRead:
			verb = "reading"
		}
		e.logf("[apply] %s %s\n", verb, id)
		step := e.recorder.StepStart(id.String(), actionFor(plan, id))
		restoreStep := e.setCurrentResourceStep(step)
		if err := e.recordSubstep(step, "create_resource", map[string]any{"resource": id.String()}, func() error {
			return e.CreateResource(ctx, id)
		}); err != nil {
			restoreStep()
			applyErr = fmt.Errorf("create %s: %w", id, err)
			e.recorder.StepFailed(step, applyErr)
			return applyErr
		}
		restoreStep()
		if err := e.recordSubstep(step, "capture_state_resource", map[string]any{"resource": id.String()}, func() error {
			e.recordStepExternal(ctx, step, id, actionFor(plan, id))
			return nil
		}); err != nil {
			applyErr = err
			e.recorder.StepFailed(step, err)
			return err
		}
		e.recorder.StepDone(step)
	}
	return nil
}
