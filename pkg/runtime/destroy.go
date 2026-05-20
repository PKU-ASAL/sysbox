package runtime

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/oslab/sysbox/pkg/graph"
)

// Destroy walks the plan reverse: tear down Destroy set in reverse topo order.
//
// The reverse topological order is computed from the dependency graph, which
// ensures correct destroy ordering regardless of how resources are ordered
// in state (drift re-creation can move resources to the end of the state
// list, breaking the old assumption that state append-order == topo order).
//
// When the graph is empty (e.g. destroy called from the API without HCL),
// we fall back to reverse state order.
//
// Resources with lifecycle.prevent_destroy = true are listed in plan.Protected
// and are silently skipped (a warning is printed to stderr).
func (e *Executor) Destroy(ctx context.Context, plan *Plan) error {
	if err := e.recorder.Begin("destroy", plan); err != nil {
		return err
	}
	var destroyErr error
	defer func() { e.recorder.Finish(destroyErr) }()

	for _, r := range plan.Protected {
		fmt.Fprintf(logWriter(e), "[lifecycle] skipping destroy of %s.%s (prevent_destroy = true)\n", r.Type, r.Name)
	}
	byID := map[string]bool{}
	for _, r := range plan.Destroy {
		byID[r.Type+"."+r.Name] = true
	}

	// Determine destroy order: prefer reverse topological from graph;
	// fall back to reverse state order when the graph is empty.
	var destroyOrder []graph.NodeID
	if len(e.graph.All()) > 0 {
		topoOrder, err := e.graph.ReverseTopoSort()
		if err != nil {
			destroyErr = fmt.Errorf("topo sort for destroy: %w", err)
			return destroyErr
		}
		destroyOrder = topoOrder
	} else {
		// Fallback: build order from state resources in reverse append order.
		for _, r := range e.state.Resources {
			destroyOrder = append(destroyOrder, graph.NodeID{Type: r.Type, Name: r.Name})
		}
		// Reverse for destroy order.
		for i, j := 0, len(destroyOrder)-1; i < j; i, j = i+1, j-1 {
			destroyOrder[i], destroyOrder[j] = destroyOrder[j], destroyOrder[i]
		}
	}

	for _, id := range destroyOrder {
		if !byID[id.String()] {
			continue
		}
		r := e.state.FindResource(id.Type, id.Name)
		if r == nil {
			continue
		}
		e.logf("[destroy] removing %s.%s\n", r.Type, r.Name)
		step := e.recorder.StepStart(id.String(), PlanActionDelete)
		if err := e.DestroyResource(ctx, *r); err != nil {
			e.logf("[destroy] warning: destroy %s.%s failed: %v\n", r.Type, r.Name, err)
			e.recorder.StepFailed(step, err)
			// Continue destroying remaining resources instead of aborting.
			// A single failure should not prevent cleanup of other resources.
		} else {
			e.recorder.StepDone(step)
		}
	}
	return nil
}

// logWriter returns the executor's logger, falling back to stderr.
func logWriter(e *Executor) io.Writer {
	if e.logger != nil {
		return e.logger
	}
	return os.Stderr
}
