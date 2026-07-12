package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/provider/network"
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

	if provider, ok := GetResourceProvider(id.Type); ok {
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
	sub, err := substrate.Get(providerName)
	if err != nil {
		result.Decision = controlplane.RecoveryDecisionUnknown
		result.Reason = "substrate not registered"
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
	obs, err := sub.ObserveNode(ctx, substrate.NodeHandle{ID: cid, Provider: providerState(sub, &current)})
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
	if ok, reason := networkAttachmentsCheck(&current); !ok {
		checks["network_attachments"] = controlplane.ResourceCheckHealth{OK: false, Reason: reason}
		result.Checks = checks
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = reason
		result.Status = state.ResourceDrifted
		return result, nil
	}
	if !nodeRoutesHealthy(ctx, sub, &current) {
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

func providerState(sub substrate.Substrate, r *state.Resource) any {
	handle, err := r.ReconstructHandle(sub)
	if err != nil {
		return nil
	}
	return handle.Provider
}

func networkAttachmentsHealthy(r *state.Resource) bool {
	items, ok := r.AttributeMap()["nics"].([]any)
	if !ok {
		return true
	}
	for _, item := range items {
		nic, _ := item.(map[string]any)
		kind := util.AsString(nic["kind"])
		switch kind {
		case "veth", "tap":
			if !network.LinkExists(util.AsString(nic["netns"]), util.AsString(nic["host_end"])) {
				return false
			}
		case "docker-nat":
			continue
		default:
			continue
		}
	}
	return true
}

func nodeRoutesHealthy(ctx context.Context, sub substrate.Substrate, r *state.Resource) bool {
	items, ok := r.AttributeMap()["routes"].([]any)
	if !ok || len(items) == 0 {
		return true
	}
	handle, err := r.ReconstructHandle(sub)
	if err != nil {
		return false
	}
	conn, err := sub.Connection(handle, nil)
	if err != nil || conn == nil {
		return false
	}
	for _, item := range items {
		route, _ := item.(map[string]any)
		dst := util.AsString(route["dst"])
		via := util.AsString(route["via"])
		if dst == "" || via == "" {
			continue
		}
		cmd := fmt.Sprintf("ip route show %s | grep -F %s", util.ShellQuote(dst), util.ShellQuote("via "+via))
		if err := conn.ExecStream(ctx, []string{cmd}, io.Discard, io.Discard); err != nil {
			return false
		}
	}
	return true
}
