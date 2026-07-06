// Package runtime is the execution engine: computes plans by diffing
// the desired graph against the current state, and executes them by
// walking the graph and calling providers.
package runtime

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
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
	// Actions is the structured plan surface used by the API and UI.
	// The Add/Change/Destroy/Unchanged index slices above remain the
	// execution contract consumed by Executor.Apply/Destroy; migrating the
	// executor walk loops to Actions-only is a separate, future task.
	Actions []controlplane.PlanAction
}

func (p *Plan) ensureActions() {
	if len(p.Actions) > 0 {
		return
	}
	for _, id := range p.Add {
		p.addAction(id, controlplane.PlanActionCreate, "resource not present in state", nil)
	}
	for _, id := range p.Change {
		p.addAction(id, controlplane.PlanActionReplace, "resource changed", nil)
	}
	for _, r := range p.Destroy {
		p.addAction(graph.NodeID{Type: r.Type, Name: r.Name}, controlplane.PlanActionDelete, "resource no longer declared", nil)
	}
	for _, id := range p.Unchanged {
		p.addAction(id, controlplane.PlanActionNoop, "", nil)
	}
}

func (p *Plan) actionsByType(types ...controlplane.PlanActionType) []controlplane.PlanAction {
	p.ensureActions()
	want := map[controlplane.PlanActionType]bool{}
	for _, typ := range types {
		want[typ] = true
	}
	out := make([]controlplane.PlanAction, 0)
	for _, action := range p.Actions {
		if want[action.Action] {
			out = append(out, action)
		}
	}
	return out
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

	for id := range inGraph {
		n := g.Get(id.Type, id.Name)
		r := s.FindResource(id.Type, id.Name)
		action, err := planActionForDesired(n, r)
		if err != nil {
			return nil, err
		}
		p.addDesiredAction(id, action)
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
				p.addAction(id, controlplane.PlanActionSkip, "blocked by lifecycle.prevent_destroy", nil)
				continue
			}
			p.Destroy = append(p.Destroy, r)
			p.addAction(id, controlplane.PlanActionDelete, "resource no longer declared", nil)
		}
	}

	return p, nil
}

func planActionForDesired(n *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	if provider, ok := GetResourceProvider(n.ID.Type); ok {
		return provider.PlanDiff(n, current)
	}
	return planDiffByDesiredHash(n, current)
}

func planDiffForDataSource(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	action, err := planDiffByDesiredHash(desired, current)
	if err != nil {
		return controlplane.PlanAction{}, err
	}
	if action.Action == controlplane.PlanActionCreate || action.Action == controlplane.PlanActionReplace {
		action.Action = controlplane.PlanActionRead
		if action.Reason == "resource not present in state" {
			action.Reason = "data source not present in state"
		}
	}
	return action, nil
}

func planDiffByDesiredHash(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	if current == nil {
		return controlplane.PlanAction{
			Resource: desired.ID.String(),
			Type:     desired.ID.Type,
			Name:     desired.ID.Name,
			Action:   controlplane.PlanActionCreate,
			Reason:   "resource not present in state",
		}, nil
	}
	action := controlplane.PlanActionNoop
	reason := ""
	var changes map[string]controlplane.FieldChange
	// Older state files do not have desired_hash. Treat them as unchanged so
	// users are not forced to rebuild every existing lab.
	if stateDesiredHash(current) != "" {
		want, err := desiredHash(desired)
		if err != nil {
			return controlplane.PlanAction{}, err
		}
		if want != stateDesiredHash(current) {
			changes, reason = diffDesiredState(desired, current)
			action = controlplane.PlanActionReplace
		}
	}
	if action == controlplane.PlanActionReplace && reason == "" {
		reason = "desired configuration changed; replacement required"
	}
	return controlplane.PlanAction{
		Resource: desired.ID.String(),
		Type:     desired.ID.Type,
		Name:     desired.ID.Name,
		Action:   action,
		Reason:   reason,
		Changes:  changes,
	}, nil
}

func (p *Plan) addDesiredAction(id graph.NodeID, action controlplane.PlanAction) {
	switch action.Action {
	case controlplane.PlanActionCreate, controlplane.PlanActionRead:
		p.Add = append(p.Add, id)
	case controlplane.PlanActionUpdate, controlplane.PlanActionReplace:
		p.Change = append(p.Change, id)
	default:
		p.Unchanged = append(p.Unchanged, id)
	}
	p.Actions = append(p.Actions, action)
}

func (p *Plan) addAction(id graph.NodeID, action controlplane.PlanActionType, reason string, changes map[string]controlplane.FieldChange) {
	p.Actions = append(p.Actions, controlplane.PlanAction{
		Resource: id.String(),
		Type:     id.Type,
		Name:     id.Name,
		Action:   action,
		Reason:   reason,
		Changes:  changes,
	})
}

// PlanFromActions rebuilds the executable plan indexes from structured actions.
// It lets API-stored plans become the execution input without recomputing a
// fresh diff.
func PlanFromActions(actions []controlplane.PlanAction, current *state.State) *Plan {
	p := &Plan{Actions: append([]controlplane.PlanAction(nil), actions...)}
	for _, action := range p.Actions {
		id := action.NodeID()
		switch action.Action {
		case controlplane.PlanActionCreate, controlplane.PlanActionRead:
			p.Add = append(p.Add, id)
		case controlplane.PlanActionUpdate, controlplane.PlanActionReplace:
			p.Change = append(p.Change, id)
		case controlplane.PlanActionDelete:
			if current != nil {
				if r := current.FindResource(action.Type, action.Name); r != nil {
					p.Destroy = append(p.Destroy, *r)
					continue
				}
			}
			p.Destroy = append(p.Destroy, state.Resource{Type: action.Type, Name: action.Name})
		case controlplane.PlanActionSkip:
			if current != nil {
				if r := current.FindResource(action.Type, action.Name); r != nil {
					p.Protected = append(p.Protected, *r)
				}
			}
		default:
			p.Unchanged = append(p.Unchanged, id)
		}
	}
	return p
}

func (p *Plan) setAction(id graph.NodeID, action controlplane.PlanActionType, reason string, changes map[string]controlplane.FieldChange) {
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
			out.setAction(id, controlplane.PlanActionCreate, "resource not present in state", nil)
		} else {
			out.Unchanged = append(out.Unchanged, id)
			out.setAction(id, controlplane.PlanActionNoop, "", nil)
		}
	}
	for _, id := range p.Change {
		if matches(id) {
			out.Change = append(out.Change, id)
			out.setAction(id, actionFor(p, id), reasonFor(p, id), changesFor(p, id))
		} else {
			out.Unchanged = append(out.Unchanged, id)
			out.setAction(id, controlplane.PlanActionNoop, "", nil)
		}
	}
	for _, r := range p.Destroy {
		if r.Type == typ && r.Name == name {
			out.Destroy = append(out.Destroy, r)
			out.setAction(graph.NodeID{Type: r.Type, Name: r.Name}, controlplane.PlanActionDelete, "resource no longer declared", nil)
		}
	}
	out.Unchanged = append(out.Unchanged, p.Unchanged...)
	for _, id := range p.Unchanged {
		out.setAction(id, controlplane.PlanActionNoop, "", nil)
	}
	return out
}

func actionFor(p *Plan, id graph.NodeID) controlplane.PlanActionType {
	for _, a := range p.Actions {
		if a.Type == id.Type && a.Name == id.Name {
			return a.Action
		}
	}
	return controlplane.PlanActionReplace
}

func reasonFor(p *Plan, id graph.NodeID) string {
	for _, a := range p.Actions {
		if a.Type == id.Type && a.Name == id.Name {
			return a.Reason
		}
	}
	return "resource changed"
}

func changesFor(p *Plan, id graph.NodeID) map[string]controlplane.FieldChange {
	for _, a := range p.Actions {
		if a.Type == id.Type && a.Name == id.Name {
			return a.Changes
		}
	}
	return nil
}

func (p *Plan) HasChanges() bool {
	for _, action := range p.Actions {
		switch action.Action {
		case controlplane.PlanActionCreate, controlplane.PlanActionRead, controlplane.PlanActionUpdate, controlplane.PlanActionReplace, controlplane.PlanActionDelete:
			return true
		}
	}
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
		if actionFor(p, id) == controlplane.PlanActionRead {
			fmt.Printf("  <= %s\n", id)
			continue
		}
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
