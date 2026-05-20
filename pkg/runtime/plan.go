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
	// Actions is the structured plan surface used by the API and future UI.
	// The legacy Add/Change/Destroy/Unchanged fields remain the execution
	// contract for now so existing CLI callers keep working.
	Actions []PlanAction
}

type PlanActionType string

const (
	PlanActionNoop    PlanActionType = "no-op"
	PlanActionCreate  PlanActionType = "create"
	PlanActionUpdate  PlanActionType = "update"
	PlanActionReplace PlanActionType = "replace"
	PlanActionDelete  PlanActionType = "delete"
	PlanActionRead    PlanActionType = "read"
	PlanActionSkip    PlanActionType = "skip"
)

type FieldChange struct {
	Before          any  `json:"before,omitempty"`
	After           any  `json:"after,omitempty"`
	RequiresReplace bool `json:"requires_replace,omitempty"`
	Sensitive       bool `json:"sensitive,omitempty"`
	Computed        bool `json:"computed,omitempty"`
}

type PlanAction struct {
	Resource string                 `json:"resource"`
	Type     string                 `json:"type"`
	Name     string                 `json:"name"`
	Action   PlanActionType         `json:"action"`
	Reason   string                 `json:"reason,omitempty"`
	Changes  map[string]FieldChange `json:"changes,omitempty"`
}

// ComputePlan diffs the graph vs state.
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
			p.addAction(id, PlanActionCreate, "resource not present in state", nil)
		} else {
			n := g.Get(id.Type, id.Name)
			r := s.FindResource(id.Type, id.Name)
			// Older state files do not have desired_hash. Treat them as
			// unchanged so users are not forced to rebuild every existing lab.
			if r != nil && stateDesiredHash(r) != "" {
				want, err := desiredHash(n)
				if err != nil {
					return nil, err
				}
				if want != stateDesiredHash(r) {
					p.Change = append(p.Change, id)
					changes, reason := diffDesiredState(n, r)
					p.addAction(id, PlanActionReplace, reason, changes)
					continue
				}
			}
			p.Unchanged = append(p.Unchanged, id)
			p.addAction(id, PlanActionNoop, "", nil)
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
			if r.LifecyclePreventDestroy() {
				p.Protected = append(p.Protected, r)
				p.addAction(id, PlanActionSkip, "blocked by lifecycle.prevent_destroy", nil)
				continue
			}
			p.Destroy = append(p.Destroy, r)
			p.addAction(id, PlanActionDelete, "resource no longer declared", nil)
		}
	}

	return p, nil
}

func (p *Plan) addAction(id graph.NodeID, action PlanActionType, reason string, changes map[string]FieldChange) {
	p.Actions = append(p.Actions, PlanAction{
		Resource: id.String(),
		Type:     id.Type,
		Name:     id.Name,
		Action:   action,
		Reason:   reason,
		Changes:  changes,
	})
}

func (p *Plan) setAction(id graph.NodeID, action PlanActionType, reason string, changes map[string]FieldChange) {
	for i := range p.Actions {
		if p.Actions[i].Type == id.Type && p.Actions[i].Name == id.Name {
			p.Actions[i].Action = action
			p.Actions[i].Reason = reason
			p.Actions[i].Changes = changes
			return
		}
	}
	p.addAction(id, action, reason, changes)
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
			out.setAction(id, PlanActionCreate, "resource not present in state", nil)
		} else {
			out.Unchanged = append(out.Unchanged, id)
			out.setAction(id, PlanActionNoop, "", nil)
		}
	}
	for _, id := range p.Change {
		if matches(id) {
			out.Change = append(out.Change, id)
			out.setAction(id, actionFor(p, id), reasonFor(p, id), changesFor(p, id))
		} else {
			out.Unchanged = append(out.Unchanged, id)
			out.setAction(id, PlanActionNoop, "", nil)
		}
	}
	for _, r := range p.Destroy {
		if r.Type == typ && r.Name == name {
			out.Destroy = append(out.Destroy, r)
			out.setAction(graph.NodeID{Type: r.Type, Name: r.Name}, PlanActionDelete, "resource no longer declared", nil)
		}
	}
	out.Unchanged = append(out.Unchanged, p.Unchanged...)
	for _, id := range p.Unchanged {
		out.setAction(id, PlanActionNoop, "", nil)
	}
	return out
}

func actionFor(p *Plan, id graph.NodeID) PlanActionType {
	for _, a := range p.Actions {
		if a.Type == id.Type && a.Name == id.Name {
			return a.Action
		}
	}
	return PlanActionReplace
}

func reasonFor(p *Plan, id graph.NodeID) string {
	for _, a := range p.Actions {
		if a.Type == id.Type && a.Name == id.Name {
			return a.Reason
		}
	}
	return "resource changed"
}

func changesFor(p *Plan, id graph.NodeID) map[string]FieldChange {
	for _, a := range p.Actions {
		if a.Type == id.Type && a.Name == id.Name {
			return a.Changes
		}
	}
	return nil
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

// PrintPlan writes a human-readable plan to w. If showProtected is true,
// resources blocked by lifecycle.prevent_destroy are also listed.
func PrintPlan(p *Plan, showProtected bool) {
	fmt.Println(p.Summary())
	for _, id := range p.Add {
		fmt.Printf("  + %s\n", id)
	}
	for _, id := range p.Change {
		reason := reasonFor(p, id)
		if reason == "" {
			reason = "changed"
		}
		fmt.Printf("  ~ %s (%s)\n", id, reason)
		for field, ch := range changesFor(p, id) {
			if ch.Sensitive {
				fmt.Printf("      %s: (sensitive) -> (sensitive)\n", field)
				continue
			}
			fmt.Printf("      %s: %v -> %v\n", field, ch.Before, ch.After)
		}
	}
	for _, r := range p.Destroy {
		fmt.Printf("  - %s.%s\n", r.Type, r.Name)
	}
	if showProtected {
		for _, r := range p.Protected {
			fmt.Printf("  ! %s.%s  (lifecycle.prevent_destroy — skipped)\n", r.Type, r.Name)
		}
	}
	for _, id := range p.Unchanged {
		fmt.Printf("    %s\n", id)
	}
}
