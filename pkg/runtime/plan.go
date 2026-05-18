// Package runtime is the execution engine: computes plans by diffing
// the desired graph against the current state, and executes them by
// walking the graph and calling providers.
package runtime

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type Plan struct {
	Add       []graph.NodeID
	Destroy   []state.Resource
	Unchanged []graph.NodeID
	// Change contains resources present in both graph and state but found
	// to be unhealthy by Refresh (drift detection). Apply will re-create them.
	Change []graph.NodeID
	// Protected lists resources that would have been destroyed but are guarded
	// by lifecycle.prevent_destroy = true. Destroy is a no-op for these.
	Protected []state.Resource
}

// ComputePlan diffs the graph vs state.
// Phase 1 simplification: no "Change" detection; any resource present in
// both is Unchanged. Phase 2 adds drift detection.
//
// Resources with lifecycle.prevent_destroy = true that would otherwise be
// destroyed (removed from HCL) are moved to Unchanged and noted in
// Plan.Protected.
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
			// Data sources are read-only; skip destroying them.
			if isDataType(r.Type) {
				continue
			}
			// Check if a lifecycle block in the graph still protects this resource.
			// (The resource was removed from HCL but is still in state.)
			// Because the resource is no longer in the graph we can't look up its
			// lifecycle from graph.Node.Data — the protection is encoded in state.
			if r.Instance != nil {
				if pd, _ := r.Instance["lifecycle_prevent_destroy"].(bool); pd {
					p.Protected = append(p.Protected, r)
					continue
				}
			}
			p.Destroy = append(p.Destroy, r)
		}
	}

	return p, nil
}

// FilterPlanByTarget returns a new Plan restricted to a single resource.
// Resources not matching type+name are moved to Unchanged.
func FilterPlanByTarget(p *Plan, typ, name string) *Plan {
	matches := func(id graph.NodeID) bool {
		return id.Type == typ && id.Name == name
	}
	out := &Plan{}
	for _, id := range p.Add {
		if matches(id) {
			out.Add = append(out.Add, id)
		} else {
			out.Unchanged = append(out.Unchanged, id)
		}
	}
	for _, id := range p.Change {
		if matches(id) {
			out.Change = append(out.Change, id)
		} else {
			out.Unchanged = append(out.Unchanged, id)
		}
	}
	for _, r := range p.Destroy {
		if r.Type == typ && r.Name == name {
			out.Destroy = append(out.Destroy, r)
		}
	}
	out.Unchanged = append(out.Unchanged, p.Unchanged...)
	return out
}

func (p *Plan) HasChanges() bool {
	return len(p.Add) > 0 || len(p.Destroy) > 0 || len(p.Change) > 0
}

// lifecycleOf extracts the LifecycleConfig from a graph node's Data, returning
// nil if the node type doesn't carry lifecycle (images, kernels, etc.).
func lifecycleOf(n *graph.Node) *config.LifecycleConfig {
	if n == nil {
		return nil
	}
	switch v := n.Data.(type) {
	case *config.NodeConfig:
		return v.Lifecycle
	case *config.NetworkConfig:
		return v.Lifecycle
	}
	return nil
}

// isDataType returns true for data source resource types (data_sysbox_node, etc.).
func isDataType(typ string) bool {
	return len(typ) > 5 && typ[:5] == "data_"
}

func (p *Plan) Summary() string {
	s := fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy, %d unchanged.",
		len(p.Add), len(p.Change), len(p.Destroy), len(p.Unchanged))
	if len(p.Protected) > 0 {
		s += fmt.Sprintf(" (%d protected by lifecycle.prevent_destroy)", len(p.Protected))
	}
	return s
}
