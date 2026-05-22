package runtime

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/oslab/sysbox/pkg/graph"
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
func (e *Executor) Refresh(ctx context.Context, plan *Plan) {
	var stillOK []graph.NodeID

	changed := map[graph.NodeID]bool{}
	for _, id := range plan.Change {
		changed[id] = true
	}

	for _, id := range plan.Unchanged {
		healthy, err := e.probeResource(ctx, id)
		if err != nil {
			// Treat probe errors as healthy to avoid spurious re-creates.
			e.logf("[refresh] %s: probe error (treating as healthy): %v\n", id, err)
			stillOK = append(stillOK, id)
			continue
		}
		if healthy {
			stillOK = append(stillOK, id)
		} else {
			e.logf("[refresh] %s: drifted - will re-create\n", id)
			changed[id] = true
			plan.Change = append(plan.Change, id)
			plan.setAction(id, PlanActionReplace, "runtime drift detected", nil)
		}
	}
	plan.Unchanged = stillOK
	e.cascadeChangedDependents(plan, changed)
}

func (e *Executor) cascadeChangedDependents(plan *Plan, changed map[graph.NodeID]bool) {
	if len(changed) == 0 || e.graph == nil {
		return
	}
	unchanged := map[graph.NodeID]bool{}
	for _, id := range plan.Unchanged {
		unchanged[id] = true
	}

	progress := true
	for progress {
		progress = false
		for _, n := range e.graph.All() {
			id := n.ID
			if changed[id] || !unchanged[id] {
				continue
			}
			for _, dep := range n.Deps {
				if changed[dep] {
					changed[id] = true
					delete(unchanged, id)
					plan.Change = append(plan.Change, id)
					plan.setAction(id, PlanActionReplace, "dependency "+dep.String()+" changed", nil)
					e.logf("[refresh] %s: dependency %s changed - will re-create\n", id, dep)
					progress = true
					break
				}
			}
		}
	}

	filtered := make([]graph.NodeID, 0, len(plan.Unchanged))
	for _, id := range plan.Unchanged {
		if unchanged[id] {
			filtered = append(filtered, id)
		}
	}
	plan.Unchanged = filtered
}

// probeResource checks whether a resource is still up.
func (e *Executor) probeResource(ctx context.Context, id graph.NodeID) (bool, error) {
	r := e.state.FindResource(id.Type, id.Name)
	if r == nil {
		return false, nil
	}

	switch id.Type {
	case "sysbox_network":
		nsName := r.Str("netns")
		brName := r.Str("bridge")
		if nsName == "" {
			return true, nil
		}
		if !network.NetnsExists(nsName) {
			return false, nil
		}
		if brName != "" && !network.BridgeExists(nsName, brName) {
			return false, nil
		}
		return true, nil

	case "sysbox_node", "sysbox_router":
		providerName := r.Provider
		sub, err := substrate.Get(providerName)
		if err != nil {
			return true, nil // substrate not registered; don't disturb
		}
		cid := r.Str("container_id")
		if cid == "" {
			return false, nil
		}
		obs, err := sub.ObserveNode(ctx, substrate.NodeHandle{ID: cid, Provider: providerState(sub, r)})
		if err != nil {
			return false, err
		}
		recovery := DecideNodeRecovery(RecoveryInput{
			Context:      RecoveryContextRefresh,
			ResourceType: id.Type,
			Provider:     providerName,
			HasState:     true,
			Observation:  obs,
		})
		switch recovery.Decision {
		case RecoveryDecisionNoop:
		case RecoveryDecisionUnknown:
			e.logf("[refresh] %s: observed status unknown reason=%s (treating as healthy)\n", id, recovery.Reason)
			return true, nil
		default:
			e.logf("[refresh] %s: observed status=%s decision=%s reason=%s\n", id, obs.Status, recovery.Decision, recovery.Reason)
			return false, nil
		}
		if !networkAttachmentsHealthy(r) {
			return false, nil
		}
		if !nodeRoutesHealthy(ctx, sub, r) {
			return false, nil
		}
		return true, nil

	case "sysbox_image":
		// Images are pulled once and don't drift in Phase 1.
		return true, nil

	case "sysbox_kernel":
		// Cache files are content-addressed; if the file disappeared,
		// the next createKernel will re-fetch. Treat present-in-state as
		// healthy.
		path := r.Str("path")
		if path == "" {
			return false, nil
		}
		if _, err := os.Stat(path); err != nil {
			return false, nil
		}
		return true, nil

	case "sysbox_firewall":
		// Phase 1: no re-apply of firewall rules on drift. Always report healthy.
		return true, nil

	default:
		return true, nil
	}
}

func providerState(sub substrate.Substrate, r *state.Resource) any {
	handle, err := r.ReconstructHandle(sub)
	if err != nil {
		return nil
	}
	return handle.Provider
}

func networkAttachmentsHealthy(r *state.Resource) bool {
	items, ok := r.Instance["nics"].([]any)
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
	items, ok := r.Instance["routes"].([]any)
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
