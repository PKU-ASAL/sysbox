package runtime

import (
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

type Plan struct {
	Actions     []model.PlannedChange `json:"actions"`
	bindings    map[string]planBinding
	excluded    map[string]bool
	stateSerial int64
	topologyUID string
	bound       bool
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
		case model.PlanActionNoop, model.PlanActionCreate, model.PlanActionRead,
			model.PlanActionReplace, model.PlanActionDelete, model.PlanActionReset, model.PlanActionUnknown:
		default:
			return fmt.Errorf("unsupported plan action %q for %s", change.Action, change.Address)
		}
	}
	return nil
}

func ComputePlan(g *graph.Graph, s *state.State, providers *driver.Registry) (*Plan, error) {
	inferManagedBridgeExposure(g, s)
	for _, resource := range s.Resources {
		if _, ok := GetResourceHandler(resource.Address.Type); !ok {
			return nil, fmt.Errorf("unsupported state resource type %q", resource.Address.Type)
		}
	}
	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("validate graph: %w", err)
	}
	if err := validateGuestFamilies(g, providers); err != nil {
		return nil, err
	}
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	plan := &Plan{}
	inGraph := make(map[string]struct{}, len(order))
	for _, resourceAddress := range order {
		inGraph[resourceAddress.String()] = struct{}{}
		node := g.Get(resourceAddress)
		if declarer, ok := GetResourceHandler(resourceAddress.Type); ok {
			if required, ok := declarer.(CapabilityDeclarer); ok {
				requirements, err := required.RequiredCapabilities(node, providers)
				if err != nil {
					return nil, err
				}
				for _, requirement := range requirements {
					if _, err := providers.Require(requirement.Driver, requirement.Capability); err != nil {
						return nil, fmt.Errorf("%s: %w", resourceAddress, err)
					}
				}
			}
		}
		change, err := planActionForDesired(node, s.FindResource(resourceAddress))
		if err != nil {
			return nil, err
		}
		if change.Action == model.PlanActionReplace && resourcePreventDestroy(g.Get(resourceAddress), s.FindResource(resourceAddress)) {
			return nil, fmt.Errorf("%s: lifecycle.prevent_destroy blocks replacement", resourceAddress)
		}
		plan.Actions = append(plan.Actions, change)
	}

	deletes := make([]model.PlannedChange, 0)
	for _, resource := range s.Resources {
		if _, exists := inGraph[resource.Address.String()]; exists || isDataType(resource.Address.Type) {
			continue
		}
		if resource.LifecyclePreventDestroy() {
			return nil, fmt.Errorf("%s: lifecycle.prevent_destroy blocks deletion", resource.Address)
		}
		deletes = append(deletes, model.PlannedChange{Address: resource.Address, Action: model.PlanActionDelete, Reason: "resource no longer declared"})
	}
	sort.Slice(deletes, func(i, j int) bool { return deletes[j].Address.Less(deletes[i].Address) })
	plan.Actions = append(plan.Actions, deletes...)
	if err := plan.bind(g, s); err != nil {
		return nil, err
	}
	return plan, plan.Validate()
}

func resourcePreventDestroy(node *graph.Node, current *state.Resource) bool {
	if node != nil {
		lifecycle := lifecycleOf(node)
		return lifecycle != nil && lifecycle.PreventDestroy
	}
	return current != nil && current.LifecyclePreventDestroy()
}

func planActionForDesired(node *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	if provider, ok := GetResourceHandler(node.Address.Type); ok {
		return provider.PlanDiff(node, current)
	}
	return planDiffByDesiredHash(node, current)
}

func planDiffForDataSource(desired *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	change, err := planDiffByDesiredHash(desired, current)
	if err != nil {
		return model.PlannedChange{}, err
	}
	if change.Action == model.PlanActionCreate || change.Action == model.PlanActionReplace {
		change.Action = model.PlanActionRead
		if change.Reason == "resource not present in state" {
			change.Reason = "data source not present in state"
		}
	}
	return change, nil
}

func planDiffByDesiredHash(desired *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	change := model.PlannedChange{Address: desired.Address, Action: model.PlanActionNoop}
	if current == nil {
		change.Action = model.PlanActionCreate
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
	change.Action = model.PlanActionReplace
	if change.Reason == "" {
		change.Reason = "desired configuration changed; replacement required"
	}
	return change, nil
}

func FilterPlanByTarget(plan *Plan, typ, name string) *Plan {
	filtered := plan.clone()
	filtered.Actions = make([]model.PlannedChange, 0, len(plan.Actions))
	filtered.excluded = map[string]bool{}
	for _, change := range plan.Actions {
		if !change.Address.Equal(address.Resource(typ, name)) {
			filtered.excluded[change.Address.String()] = true
			if change.Action != model.PlanActionDelete {
				change.Action = model.PlanActionNoop
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
		case model.PlanActionCreate, model.PlanActionRead, model.PlanActionReplace, model.PlanActionDelete, model.PlanActionReset, model.PlanActionUnknown:
			return true
		}
	}
	return false
}

func (p Plan) ChangeFor(resourceAddress address.Address) (model.PlannedChange, bool) {
	for _, change := range p.Actions {
		if change.Address.Equal(resourceAddress) {
			return change, true
		}
	}
	return model.PlannedChange{}, false
}

func (p Plan) Summary() string {
	counts := map[model.PlanActionType]int{}
	for _, change := range p.Actions {
		counts[change.Action]++
	}
	if counts[model.PlanActionReset] > 0 {
		return fmt.Sprintf("Plan: %d to reset.", counts[model.PlanActionReset])
	}
	return fmt.Sprintf("Plan: %d to add, %d to replace, %d to destroy, %d unchanged.", counts[model.PlanActionCreate]+counts[model.PlanActionRead], counts[model.PlanActionReplace], counts[model.PlanActionDelete], counts[model.PlanActionNoop])
}

func PrintPlan(plan *Plan, _ bool) {
	fmt.Println(plan.Summary())
	for _, change := range plan.Actions {
		symbol := map[model.PlanActionType]string{
			model.PlanActionCreate: "+", model.PlanActionRead: "<=", model.PlanActionReplace: "-/+",
			model.PlanActionDelete: "-", model.PlanActionNoop: " ", model.PlanActionUnknown: "?",
			model.PlanActionReset: "~>",
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
