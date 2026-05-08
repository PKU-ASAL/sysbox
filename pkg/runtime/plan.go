// Package runtime is the execution engine: computes plans by diffing
// the desired graph against the current state, and executes them by
// walking the graph and calling providers.
package runtime

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type Plan struct {
	Add       []graph.NodeID
	Destroy   []state.Resource
	Unchanged []graph.NodeID
}

// ComputePlan diffs the graph vs state.
// Phase 1 simplification: no "Change" detection; any resource present in
// both is Unchanged. Phase 2 adds drift detection.
func ComputePlan(g *graph.Graph, s *state.State) (*Plan, error) {
	p := &Plan{}

	inGraph := map[graph.NodeID]bool{}
	for _, n := range g.All() {
		inGraph[n.ID] = true
	}

	inState := map[graph.NodeID]bool{}
	for _, r := range s.Resources {
		inState[graph.NodeID{Type: r.Type, Name: r.Name}] = true
	}

	for id := range inGraph {
		if !inState[id] {
			p.Add = append(p.Add, id)
		} else {
			p.Unchanged = append(p.Unchanged, id)
		}
	}

	for _, r := range s.Resources {
		id := graph.NodeID{Type: r.Type, Name: r.Name}
		if !inGraph[id] {
			p.Destroy = append(p.Destroy, r)
		}
	}

	return p, nil
}

func (p *Plan) HasChanges() bool {
	return len(p.Add) > 0 || len(p.Destroy) > 0
}

func (p *Plan) Summary() string {
	return fmt.Sprintf("Plan: %d to add, %d to destroy, %d unchanged.",
		len(p.Add), len(p.Destroy), len(p.Unchanged))
}
