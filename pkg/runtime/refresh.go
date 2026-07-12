package runtime

import (
	"context"
	"fmt"
	"io"

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
	changed := map[string]bool{}
	for i := range refreshed.Actions {
		change := &refreshed.Actions[i]
		if change.Action == controlplane.PlanActionReplace {
			changed[change.Address.String()] = true
			continue
		}
		if change.Action != controlplane.PlanActionNoop {
			continue
		}
		healthy, err := e.probeResource(ctx, change.Address)
		if err != nil {
			change.Action = controlplane.PlanActionUnknown
			change.Reason = "probe failed: " + err.Error()
			continue
		}
		if !healthy {
			e.logf("[refresh] %s: drifted - will re-create\n", change.Address)
			changed[change.Address.String()] = true
			change.Action = controlplane.PlanActionReplace
			change.Reason = "runtime drift detected"
		}
	}
	e.cascadeChangedDependents(refreshed, changed)
	return refreshed, refreshed.Validate()
}

func (e *Executor) cascadeChangedDependents(plan *Plan, changed map[string]bool) {
	if len(changed) == 0 || e.graph == nil {
		return
	}
	unchanged := map[string]int{}
	for i, change := range plan.Actions {
		if change.Action == controlplane.PlanActionNoop {
			unchanged[change.Address.String()] = i
		}
	}

	progress := true
	for progress {
		progress = false
		for _, n := range e.graph.All() {
			id := n.Address
			index, isUnchanged := unchanged[id.String()]
			if changed[id.String()] || !isUnchanged {
				continue
			}
			for _, dep := range n.Deps {
				if changed[dep.String()] {
					changed[id.String()] = true
					delete(unchanged, id.String())
					plan.Actions[index].Action = controlplane.PlanActionReplace
					plan.Actions[index].Reason = "dependency changed"
					plan.Actions[index].DependencyReason = dep.String()
					e.logf("[refresh] %s: dependency %s changed - will re-create\n", id, dep)
					progress = true
					break
				}
			}
		}
	}

}

// probeResource checks whether a resource is still up.
func (e *Executor) probeResource(ctx context.Context, id address.Address) (bool, error) {
	r := e.state.FindResource(id)
	if r == nil {
		return false, nil
	}

	if provider, ok := GetResourceProvider(id.Type); ok {
		if _, err := provider.Read(ctx, *r); err != nil {
			status, _, known := classifyResourceReadError(err)
			if known && status == ResourceReadDrifted {
				return false, nil
			}
			return true, err
		}
		return true, nil
	}

	return true, nil
}

func readNodeLikeResource(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	providerName := current.Driver
	sub, err := substrate.Get(providerName)
	if err != nil {
		result.Decision = controlplane.RecoveryDecisionUnknown
		result.Reason = "substrate not registered"
		return result, unknownResource("substrate not registered", err)
	}
	cid := current.Str("container_id")
	if cid == "" {
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = "node has no persisted external id"
		return result, driftedResource("node has no persisted external id")
	}
	obs, err := sub.ObserveNode(ctx, substrate.NodeHandle{ID: cid, Provider: providerState(sub, &current)})
	if err != nil {
		result.Decision = controlplane.RecoveryDecisionUnknown
		result.Reason = "observe node"
		return result, unknownResource("observe node", err)
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
		return result, unknownResource(recovery.Reason, nil)
	default:
		return result, driftedResource(recovery.Reason)
	}
	checks := map[string]controlplane.ResourceCheckHealth{}
	if ok, reason := networkAttachmentsCheck(&current); !ok {
		checks["network_attachments"] = controlplane.ResourceCheckHealth{OK: false, Reason: reason}
		result.Checks = checks
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = reason
		return result, driftedResource(reason)
	}
	if !nodeRoutesHealthy(ctx, sub, &current) {
		checks["routes"] = controlplane.ResourceCheckHealth{OK: false, Reason: "route missing"}
		result.Checks = checks
		result.Decision = controlplane.RecoveryDecisionMarkDrift
		result.Reason = "route missing"
		return result, driftedResource("route missing")
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
	items, ok := r.Attributes["nics"].([]any)
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
	items, ok := r.Attributes["routes"].([]any)
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
