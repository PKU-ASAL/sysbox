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
	refreshed := plan.clone()
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
		if action, dictates := planActionForObservedStatus(result.Status); dictates {
			if action == controlplane.PlanActionReplace {
				e.logf("[refresh] %s: drifted - will re-create\n", change.Address)
			}
			change.Action = action
			change.Reason = result.Reason
			if change.Reason == "" && action == controlplane.PlanActionReplace {
				change.Reason = "runtime drift detected"
			}
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
		result, err := readResourceWithAttachments(ctx, provider, *r, e.state)
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
	cid := current.ContainerID()
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
	if obs.Status == substrate.NodeStatusMissing {
		result.Status = state.ResourceAbsent
		result.Reason = obs.Reason
		return result, nil
	}
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
	result.Resource = current
	routesHealthy, routeErr := nodeRoutesHealthy(ctx, nodeDriver, stateDriver, &current)
	if routeErr != nil {
		checks["routes"] = controlplane.ResourceCheckHealth{OK: false, Reason: "route observation unavailable"}
		result.Checks = checks
		result.Decision = controlplane.RecoveryDecisionUnknown
		result.Reason = "route observation unavailable"
		result.Status = state.ResourceUnknown
		return result, fmt.Errorf("observe routes: %w", routeErr)
	}
	if !routesHealthy {
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

// readResourceWithAttachments combines resource-local observation with checks
// that require the canonical topology's network identity and provider state.
func readResourceWithAttachments(ctx context.Context, handler ResourceHandler, current state.Resource, st *state.State) (ResourceReadResult, error) {
	if current.OwnerUID != "" {
		id := substrate.OperationIdentity{ActionID: "observe", TopologyUID: current.OwnerUID, OwnerUID: current.OwnerUID, ResourceUID: current.ResourceUID, Generation: current.Generation}
		if existing, ok := substrate.OperationIdentityFromContext(ctx); ok {
			id.ActionID = existing.ActionID
		}
		ctx = substrate.WithOperationIdentity(ctx, id)
	}
	result, err := handler.Read(ctx, cloneResetStateResource(current))
	// Drift permits replacement, so attachment uncertainty must still be
	// checked before reporting it as a safe reconciliation decision.
	if err != nil || (result.Status != state.ResourcePresent && result.Status != state.ResourceDrifted) || len(current.Attachments) == 0 {
		return result, err
	}
	observed := cloneResetStateResource(current)
	if !result.Resource.Address.IsZero() {
		observed = cloneResetStateResource(result.Resource)
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(current.Driver)
	var handle substrate.NodeHandle
	if err == nil {
		handle, err = observed.ReconstructHandle(stateDriver)
	}
	status, reason := state.ResourceUnknown, ""
	if err == nil {
		status, reason, err = observeAttachments(ctx, handle, &observed, st)
	} else {
		reason = err.Error()
	}
	result.Resource = observed
	if status != state.ResourcePresent {
		if result.Checks == nil {
			result.Checks = map[string]controlplane.ResourceCheckHealth{}
		}
		result.Checks["network_attachments"] = controlplane.ResourceCheckHealth{OK: false, Reason: reason}
		result.Status, result.Reason = status, reason
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		if status == state.ResourceUnknown {
			result.Decision = controlplane.RecoveryDecisionUnknown
		}
	}
	return result, err
}

func observeAttachments(ctx context.Context, handle substrate.NodeHandle, r *state.Resource, st *state.State) (state.ResourceStatus, string, error) {
	if len(r.Attachments) == 0 {
		return state.ResourcePresent, "", nil
	}
	nic, err := driver.DefaultRegistry.RequireNIC(r.Driver)
	if err != nil {
		return state.ResourceUnknown, err.Error(), err
	}
	for i := range r.Attachments {
		a := &r.Attachments[i]
		request, err := attachmentRequestFromState(st, *a)
		if err != nil {
			return state.ResourceUnknown, err.Error(), err
		}
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

func nodeRoutesHealthy(ctx context.Context, nodeDriver driver.Node, stateDriver driver.NodeState, r *state.Resource) (bool, error) {
	items, ok := r.AttributeMap()["routes"].([]any)
	if !ok || len(items) == 0 {
		return true, nil
	}
	handle, err := r.ReconstructHandle(stateDriver)
	if err != nil {
		return false, err
	}
	guestNetwork, err := driver.DefaultRegistry.RequireGuestNetwork(r.Driver)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		route, _ := item.(map[string]any)
		dst := util.AsString(route["dst"])
		via := util.AsString(route["via"])
		if dst == "" || via == "" {
			continue
		}
		ok, err := guestNetwork.HasRoute(ctx, handle, dst, via)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// planActionForObservedStatus maps an observed resource status to the plan
// action it dictates. dictates is false for statuses that leave the planned
// action alone.
//
// This encodes the architecture invariant "Unknown observation 不等于 absent".
// absent and drifted authorise a replace; unknown must produce
// PlanActionUnknown, which apply and destroy both refuse. A provider that is
// briefly unreachable or lacks permission is not evidence that the object is
// gone, and collapsing the two here is what causes duplicate creation and
// wrong deletion.
//
// It is a named function rather than an inline switch so the invariant can be
// asserted directly; see unknown_not_absent_test.go.
func planActionForObservedStatus(status state.ResourceStatus) (controlplane.PlanActionType, bool) {
	switch status {
	case state.ResourceAbsent, state.ResourceDrifted:
		return controlplane.PlanActionReplace, true
	case state.ResourceUnknown:
		return controlplane.PlanActionUnknown, true
	default:
		// Degraded resources remain present; health exposes the degradation.
		return "", false
	}
}
