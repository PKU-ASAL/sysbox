package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// Refresh queries each resource in Plan.Unchanged against the real world.
// Resources that are found to be missing or unhealthy are promoted to
// Plan.Change so Apply can re-create them.
//
// Refresh is a best-effort pass: a probe error (e.g. transient timeout)
// is treated as "unknown" and the resource stays Unchanged.
func (e *Executor) Refresh(ctx context.Context, plan *Plan) (*Plan, error) {
	refreshed := &Plan{Actions: append([]controlplane.PlannedChange(nil), plan.Actions...)}
	for i := range refreshed.Actions {
		change := &refreshed.Actions[i]
		if change.Action == controlplane.PlanActionReplace {
			continue
		}
		if change.Action != controlplane.PlanActionNoop {
			continue
		}
		result, err := e.observeResource(ctx, change.Address)
		if current := e.state.FindResource(change.Address); current != nil {
			current.Status = result.Status
			if !result.Resource.Address.IsZero() {
				current.Attributes = result.Resource.Attributes
				current.ExternalID = result.Resource.ExternalID
				current.Attachments = cloneAttachments(result.Resource.Attachments)
				current.UpdatedAt = time.Now().UTC()
			}
		}
		if err != nil {
			change.Action = controlplane.PlanActionUnknown
			change.Reason = "probe failed: " + err.Error()
			continue
		}
		switch result.Status {
		case state.ResourceAbsent, state.ResourceDrifted:
			e.logf("[refresh] %s: drifted - will re-create\n", change.Address)
			change.Action = controlplane.PlanActionReplace
			change.Reason = result.Reason
			if change.Reason == "" {
				change.Reason = "runtime drift detected"
			}
		case state.ResourceUnknown:
			change.Action = controlplane.PlanActionUnknown
			change.Reason = result.Reason
		case state.ResourceDegraded, state.ResourcePresent:
			// Degraded resources remain present; health exposes the degradation.
		}
	}
	return refreshed, refreshed.Validate()
}

func (e *Executor) RefreshAndPersist(ctx context.Context, plan *Plan, manager *state.Manager) (*Plan, error) {
	refreshed, err := e.Refresh(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := manager.SaveWithContext(ctx, e.state); err != nil {
		return nil, fmt.Errorf("persist refreshed state: %w", err)
	}
	return refreshed, nil
}

// probeResource checks whether a resource is still up.
func (e *Executor) observeResource(ctx context.Context, id address.Address) (ResourceReadResult, error) {
	r := e.state.FindResource(id)
	if r == nil {
		return ResourceReadResult{Status: state.ResourceAbsent, Reason: "resource absent from state"}, nil
	}

	if provider, ok := GetResourceHandler(id.Type); ok {
		result, err := provider.Read(ctx, *r)
		if err != nil {
			result.Status = state.ResourceUnknown
			if result.Reason == "" {
				result.Reason = err.Error()
			}
			return result, err
		}
		if result.Status == "" {
			result.Status = state.ResourcePresent
		}
		return result, nil
	}
	return ResourceReadResult{Status: state.ResourcePresent, Resource: *r}, nil
}

func readNodeLikeResource(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	providerName := current.Driver
	nodeDriver, err := driver.DefaultRegistry.RequireNode(providerName)
	if err != nil {
		result.Decision = controlplane.RecoveryDecisionUnknown
		result.Reason = "node driver unavailable"
		result.Status = state.ResourceUnknown
		return result, err
	}
	cid := current.Str("container_id")
	if cid == "" {
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = "node has no persisted external id"
		result.Status = state.ResourceDrifted
		return result, nil
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(providerName)
	if err != nil {
		result.Status = state.ResourceUnknown
		return result, err
	}
	handle, err := current.ReconstructHandle(stateDriver)
	if err != nil {
		result.Status = state.ResourceUnknown
		return result, err
	}
	obs, err := nodeDriver.ObserveNode(ctx, handle)
	if err != nil {
		result.Decision = controlplane.RecoveryDecisionUnknown
		result.Reason = "observe node"
		result.Status = state.ResourceUnknown
		return result, err
	}
	result.Observation = &obs
	recovery := DecideNodeRecovery(RecoveryInput{
		Context:      RecoveryContextRefresh,
		ResourceType: current.Address.Type,
		Provider:     providerName,
		HasState:     true,
		Observation:  obs,
	})
	result.Decision = recovery.Decision
	result.Reason = recovery.Reason
	switch recovery.Decision {
	case controlplane.RecoveryDecisionNoop:
	case controlplane.RecoveryDecisionUnknown:
		result.Status = state.ResourceUnknown
		return result, nil
	default:
		result.Status = state.ResourceDrifted
		return result, nil
	}
	checks := map[string]controlplane.ResourceCheckHealth{}
	if status, reason, err := observeAttachments(ctx, handle, &current); status != state.ResourcePresent {
		checks["network_attachments"] = controlplane.ResourceCheckHealth{OK: false, Reason: reason}
		result.Checks = checks
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = reason
		result.Status = status
		if err != nil {
			return result, err
		}
		return result, nil
	}
	result.Resource = current
	if !nodeRoutesHealthy(ctx, nodeDriver, stateDriver, &current) {
		checks["routes"] = controlplane.ResourceCheckHealth{OK: false, Reason: "route missing"}
		result.Checks = checks
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = "route missing"
		result.Status = state.ResourceDrifted
		return result, nil
	}
	if len(checks) > 0 {
		result.Checks = checks
	}
	return result, nil
}

func observeAttachments(ctx context.Context, handle substrate.NodeHandle, r *state.Resource) (state.ResourceStatus, string, error) {
	if len(r.Attachments) == 0 {
		return state.ResourcePresent, "", nil
	}
	nic, err := driver.DefaultRegistry.RequireNIC(r.Driver)
	if err != nil {
		return state.ResourceUnknown, err.Error(), err
	}
	for i := range r.Attachments {
		a := &r.Attachments[i]
		request := driver.AttachmentRequest{Name: a.Name, Network: a.Network, MAC: a.MAC, IPPrefixes: append([]string(nil), a.IPPrefixes...), Gateway: a.Gateway}
		observed, err := nic.Observe(ctx, handle, request, a.DriverState)
		if err != nil {
			if driver.IsCategory(err, driver.ErrorNotFound) {
				return state.ResourceDrifted, fmt.Sprintf("attachment %s missing", a.Name), nil
			}
			return state.ResourceUnknown, fmt.Sprintf("observe attachment %s: %v", a.Name, err), err
		}
		a.Observation.GuestDevice = observed.GuestDevice
		if len(observed.State) > 0 {
			a.DriverState = observed.State
		}
	}
	return state.ResourcePresent, "", nil
}

func nodeRoutesHealthy(ctx context.Context, nodeDriver driver.Node, stateDriver driver.NodeState, r *state.Resource) bool {
	items, ok := r.AttributeMap()["routes"].([]any)
	if !ok || len(items) == 0 {
		return true
	}
	handle, err := r.ReconstructHandle(stateDriver)
	if err != nil {
		return false
	}
	guestNetwork, err := driver.DefaultRegistry.RequireGuestNetwork(r.Driver)
	if err != nil {
		return false
	}
	for _, item := range items {
		route, _ := item.(map[string]any)
		dst := util.AsString(route["dst"])
		via := util.AsString(route["via"])
		if dst == "" || via == "" {
			continue
		}
		ok, err := guestNetwork.HasRoute(ctx, handle, dst, via)
		if err != nil || !ok {
			return false
		}
	}
	return true
}
