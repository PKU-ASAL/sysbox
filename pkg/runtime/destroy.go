package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Destroy walks the plan reverse: tear down Destroy set in reverse topo order.
//
// Resources in state.Resources were appended in forward topo order during
// apply, so reversing the slice gives us reverse-dependency destroy order.
//
// Resources with lifecycle.prevent_destroy = true are listed in plan.Protected
// and are silently skipped (a warning is printed to stderr).
func (e *Executor) Destroy(ctx context.Context, plan *Plan) error {
	for _, r := range plan.Protected {
		fmt.Fprintf(logWriter(e), "[lifecycle] skipping destroy of %s.%s (prevent_destroy = true)\n", r.Type, r.Name)
	}
	byID := map[string]bool{}
	for _, r := range plan.Destroy {
		byID[r.Type+"."+r.Name] = true
	}

	// Snapshot the resource list so we can iterate while DestroyResource
	// mutates e.state.Resources.
	// Note: plan.Protected resources are NOT in plan.Destroy, so we simply
	// skip them here without any extra check.
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
		e.logf("[destroy] removing %s.%s\n", r.Type, r.Name)
		if err := e.DestroyResource(ctx, *r); err != nil {
			return fmt.Errorf("destroy %s.%s: %w", r.Type, r.Name, err)
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
