package runtime

import (
	"context"
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Refresh queries each resource in Plan.Unchanged against the real world.
// Resources that are found to be missing or unhealthy are promoted to
// Plan.Change so Apply can re-create them.
//
// Refresh is a best-effort pass: a probe error (e.g. transient timeout)
// is treated as "unknown" and the resource stays Unchanged.
func (e *Executor) Refresh(ctx context.Context, plan *Plan) {
	var stillOK []graph.NodeID

	for _, id := range plan.Unchanged {
		healthy, err := e.probeResource(ctx, id)
		if err != nil {
			// Treat probe errors as healthy to avoid spurious re-creates.
			fmt.Printf("[refresh] %s: probe error (treating as healthy): %v\n", id, err)
			stillOK = append(stillOK, id)
			continue
		}
		if healthy {
			stillOK = append(stillOK, id)
		} else {
			fmt.Printf("[refresh] %s: drifted — will re-create\n", id)
			plan.Change = append(plan.Change, id)
		}
	}
	plan.Unchanged = stillOK
}

// probeResource checks whether a resource is still up.
func (e *Executor) probeResource(ctx context.Context, id graph.NodeID) (bool, error) {
	r := e.state.FindResource(id.Type, id.Name)
	if r == nil {
		return false, nil
	}

	switch id.Type {
	case "sysbox_network":
		nsName := asString(r.Instance["netns"])
		brName := asString(r.Instance["bridge"])
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
		cid := asString(r.Instance["container_id"])
		if cid == "" {
			return false, nil
		}
		return sub.NodeStatus(ctx, substrate.NodeHandle{ID: cid})

	case "sysbox_image":
		// Images are pulled once and don't drift in Phase 1.
		return true, nil

	case "sysbox_kernel":
		// Cache files are content-addressed; if the file disappeared,
		// the next createKernel will re-fetch. Treat present-in-state as
		// healthy.
		path := asString(r.Instance["path"])
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
