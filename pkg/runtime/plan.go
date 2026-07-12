package runtime

import (
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type Plan struct {
	Actions []controlplane.PlannedChange `json:"actions"`
}

func (p Plan) Validate() error {
	seen := make(map[string]struct{}, len(p.Actions))
	for _, change := range p.Actions {
		if change.Address.IsZero() {
			return fmt.Errorf("plan action has an empty address")
		}
		if _, exists := seen[change.Address.String()]; exists {
			return fmt.Errorf("duplicate plan action for %s", change.Address)
		}
		seen[change.Address.String()] = struct{}{}
		switch change.Action {
		case controlplane.PlanActionNoop, controlplane.PlanActionCreate, controlplane.PlanActionRead,
			controlplane.PlanActionReplace, controlplane.PlanActionDelete, controlplane.PlanActionUnknown:
		default:
			return fmt.Errorf("unsupported plan action %q for %s", change.Action, change.Address)
		}
	}
	return nil
}

func ComputePlan(g *graph.Graph, s *state.State) (*Plan, error) {
	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("validate graph: %w", err)
	}
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	plan := &Plan{}
	inGraph := make(map[string]struct{}, len(order))
	for _, resourceAddress := range order {
		inGraph[resourceAddress.String()] = struct{}{}
		change, err := planActionForDesired(g.Get(resourceAddress), s.FindResource(resourceAddress))
		if err != nil {
			return nil, err
		}
		if change.Action == controlplane.PlanActionReplace && resourcePreventDestroy(g.Get(resourceAddress), s.FindResource(resourceAddress)) {
			return nil, fmt.Errorf("%s: lifecycle.prevent_destroy blocks replacement", resourceAddress)
		}
		plan.Actions = append(plan.Actions, change)
	}

	deletes := make([]controlplane.PlannedChange, 0)
	for _, resource := range s.Resources {
		if _, exists := inGraph[resource.Address.String()]; exists || isDataType(resource.Address.Type) {
			continue
		}
		if resource.LifecyclePreventDestroy() {
			return nil, fmt.Errorf("%s: lifecycle.prevent_destroy blocks deletion", resource.Address)
		}
		deletes = append(deletes, controlplane.PlannedChange{Address: resource.Address, Action: controlplane.PlanActionDelete, Reason: "resource no longer declared"})
	}
	sort.Slice(deletes, func(i, j int) bool { return deletes[j].Address.Less(deletes[i].Address) })
	plan.Actions = append(plan.Actions, deletes...)
	return plan, plan.Validate()
}

func resourcePreventDestroy(node *graph.Node, current *state.Resource) bool {
	if node != nil {
		lifecycle := lifecycleOf(node)
		return lifecycle != nil && lifecycle.PreventDestroy
	}
	return current != nil && current.LifecyclePreventDestroy()
}

func planActionForDesired(node *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	if provider, ok := GetResourceHandler(node.Address.Type); ok {
		return provider.PlanDiff(node, current)
	}
	return planDiffByDesiredHash(node, current)
}

func planDiffForDataSource(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	change, err := planDiffByDesiredHash(desired, current)
	if err != nil {
		return controlplane.PlannedChange{}, err
	}
	if change.Action == controlplane.PlanActionCreate || change.Action == controlplane.PlanActionReplace {
		change.Action = controlplane.PlanActionRead
		if change.Reason == "resource not present in state" {
			change.Reason = "data source not present in state"
		}
	}
	return change, nil
}

func planDiffByDesiredHash(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	change := controlplane.PlannedChange{Address: desired.Address, Action: controlplane.PlanActionNoop}
	if current == nil {
		change.Action = controlplane.PlanActionCreate
		change.Reason = "resource not present in state"
		return change, nil
	}
	if current.AttributeMap()[desiredPayloadKey] == nil {
		return change, nil
	}
	change.Changes, change.Reason = diffDesiredState(desired, current)
	if len(change.Changes) == 0 {
		return change, nil
	}
	change.Action = controlplane.PlanActionReplace
	if change.Reason == "" {
		change.Reason = "desired configuration changed; replacement required"
	}
	return change, nil
}

func FilterPlanByTarget(plan *Plan, typ, name string) *Plan {
	filtered := &Plan{Actions: make([]controlplane.PlannedChange, 0, len(plan.Actions))}
	for _, change := range plan.Actions {
		if change.Address.Type != typ || change.Address.Name != name {
			if change.Action != controlplane.PlanActionDelete {
				change.Action = controlplane.PlanActionNoop
				change.Reason = ""
				change.Changes = nil
				filtered.Actions = append(filtered.Actions, change)
			}
			continue
		}
		filtered.Actions = append(filtered.Actions, change)
	}
	return filtered
}

func (p Plan) HasChanges() bool {
	for _, change := range p.Actions {
		switch change.Action {
		case controlplane.PlanActionCreate, controlplane.PlanActionRead, controlplane.PlanActionReplace, controlplane.PlanActionDelete, controlplane.PlanActionUnknown:
			return true
		}
	}
	return false
}

func (p Plan) ChangeFor(resourceAddress address.Address) (controlplane.PlannedChange, bool) {
	for _, change := range p.Actions {
		if change.Address.Equal(resourceAddress) {
			return change, true
		}
	}
	return controlplane.PlannedChange{}, false
}

func (p Plan) Summary() string {
	counts := map[controlplane.PlanActionType]int{}
	for _, change := range p.Actions {
		counts[change.Action]++
	}
	return fmt.Sprintf("Plan: %d to add, %d to replace, %d to destroy, %d unchanged.", counts[controlplane.PlanActionCreate]+counts[controlplane.PlanActionRead], counts[controlplane.PlanActionReplace], counts[controlplane.PlanActionDelete], counts[controlplane.PlanActionNoop])
}

func PrintPlan(plan *Plan, _ bool) {
	fmt.Println(plan.Summary())
	for _, change := range plan.Actions {
		symbol := map[controlplane.PlanActionType]string{
			controlplane.PlanActionCreate: "+", controlplane.PlanActionRead: "<=", controlplane.PlanActionReplace: "-/+",
			controlplane.PlanActionDelete: "-", controlplane.PlanActionNoop: " ", controlplane.PlanActionUnknown: "?",
		}[change.Action]
		fmt.Printf("  %s %s", symbol, change.Address)
		if change.Reason != "" {
			fmt.Printf(" (%s)", change.Reason)
		}
		fmt.Println()
		for _, field := range change.Changes {
			if field.Sensitive {
				fmt.Printf("      %s: (sensitive) -> (sensitive)\n", field.Path)
			} else {
				fmt.Printf("      %s: %v -> %v\n", field.Path, field.Before, field.After)
			}
		}
	}
}

func lifecycleOf(node *graph.Node) *config.LifecycleConfig {
	if node == nil {
		return nil
	}
	switch value := node.Data.(type) {
	case *config.NodeConfig:
		return value.Lifecycle
	case *config.NetworkConfig:
		return value.Lifecycle
	}
	return nil
}

func isDataType(typ string) bool { return len(typ) > 5 && typ[:5] == "data_" }
