package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/graph"
)

// Apply walks the plan forward: create Add resources and re-create Change
// (drifted) resources, both in topo order.
func (e *Executor) Apply(ctx context.Context, plan *Plan) error {
	if err := e.graph.Validate(); err != nil {
		return fmt.Errorf("graph validation: %w", err)
	}
	order, err := e.graph.TopoSort()
	if err != nil {
		return err
	}

	toCreate := map[string]bool{}
	for _, id := range plan.Add {
		toCreate[id.String()] = true
	}

	// For drifted resources, destroy all affected existing resources in
	// reverse topo order first (dependents before dependencies), then recreate
	// them in normal topo order below.
	changeSet := map[graph.NodeID]bool{}
	for _, id := range plan.Change {
		changeSet[id] = true
		toCreate[id.String()] = true
	}
	if len(changeSet) > 0 {
		reverse, err := e.graph.ReverseTopoSort()
		if err != nil {
			return err
		}
		for _, id := range reverse {
			if !changeSet[id] {
				continue
			}
			r := e.state.FindResource(id.Type, id.Name)
			if r != nil {
				e.logf("[apply] removing drifted %s before re-create\n", id)
				if err := e.DestroyResource(ctx, *r); err != nil {
					e.logf("[apply] warning: cleanup of drifted %s failed: %v\n", id, err)
				}
			}
		}
	}

	for _, id := range order {
		if !toCreate[id.String()] {
			continue
		}
		verb := "creating"
		for _, c := range plan.Change {
			if c == id {
				verb = "re-creating"
				break
			}
		}
		e.logf("[apply] %s %s\n", verb, id)
		if err := e.CreateResource(ctx, id); err != nil {
			return fmt.Errorf("create %s: %w", id, err)
		}
	}
	return nil
}
