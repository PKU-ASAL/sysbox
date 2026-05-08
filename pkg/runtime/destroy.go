package runtime

import (
	"context"
	"fmt"
)

// Destroy walks the plan reverse: tear down Destroy set in reverse topo order.
//
// Resources in state.Resources were appended in forward topo order during
// apply, so reversing the slice gives us reverse-dependency destroy order.
func (e *Executor) Destroy(ctx context.Context, plan *Plan) error {
	byID := map[string]bool{}
	for _, r := range plan.Destroy {
		byID[r.Type+"."+r.Name] = true
	}

	// Snapshot the resource list so we can iterate while DestroyResource
	// mutates e.state.Resources.
	snapshot := append([]struct {
		Type string
		Name string
	}{}, func() []struct {
		Type string
		Name string
	} {
		out := make([]struct {
			Type string
			Name string
		}, 0, len(e.state.Resources))
		for _, r := range e.state.Resources {
			out = append(out, struct {
				Type string
				Name string
			}{r.Type, r.Name})
		}
		return out
	}()...)

	for i := len(snapshot) - 1; i >= 0; i-- {
		key := snapshot[i].Type + "." + snapshot[i].Name
		if !byID[key] {
			continue
		}
		r := e.state.FindResource(snapshot[i].Type, snapshot[i].Name)
		if r == nil {
			continue
		}
		fmt.Printf("[destroy] removing %s.%s\n", r.Type, r.Name)
		if err := e.DestroyResource(ctx, *r); err != nil {
			return fmt.Errorf("destroy %s.%s: %w", r.Type, r.Name, err)
		}
	}
	return nil
}
