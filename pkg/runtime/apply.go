package runtime

import (
	"context"
	"fmt"
)

// Apply walks the plan forward: create everything in Add in topo order.
func (e *Executor) Apply(ctx context.Context, plan *Plan) error {
	order, err := e.graph.TopoSort()
	if err != nil {
		return err
	}

	toAdd := map[string]bool{}
	for _, id := range plan.Add {
		toAdd[id.String()] = true
	}

	for _, id := range order {
		if !toAdd[id.String()] {
			continue
		}
		fmt.Printf("[apply] creating %s\n", id)
		if err := e.CreateResource(ctx, id); err != nil {
			return fmt.Errorf("create %s: %w", id, err)
		}
	}
	return nil
}
