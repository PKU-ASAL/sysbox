package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// -- sysbox_monitor --

// createMonitor records the monitor intent in state. No EDR agent is deployed
// here; activation happens when `sysbox sensor start` reads the state and
// calls the registered MonitorBackend.Start().
//
// Separating declaration (Apply) from activation (sensor start) lets the same
// HCL field describe the monitoring topology without coupling the lifecycle to
// the apply graph — backends can be hot-restarted between episodes without
// re-applying the entire lab.
func (e *Executor) createMonitor(_ context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.MonitorConfig)
	if !ok {
		return fmt.Errorf("monitor %s: wrong data type", n.ID)
	}

	backend := cfg.Backend
	if backend == "" {
		backend = "tracee"
	}

	// Validate all referenced nodes exist at apply time.
	var nodeNames []string
	for _, nodeRef := range cfg.Nodes {
		nodeName := config.ResolveName(nodeRef)
		if nodeName == "" {
			return fmt.Errorf("monitor %s: cannot resolve node ref %q", n.ID.Name, nodeRef)
		}
		if e.state.FindResource("sysbox_node", nodeName) == nil {
			return fmt.Errorf("monitor %s: node %s not applied yet", n.ID.Name, nodeName)
		}
		nodeNames = append(nodeNames, nodeName)
	}

	// Store intent only: node names + backend config.
	// Runtime handles (container_id, mntns) are resolved dynamically at
	// sensor start so they always reflect the current node state, even
	// after a node is reprovisioned with a new container ID.
	e.state.AddResource(state.Resource{
		Type:     "sysbox_monitor",
		Name:     n.ID.Name,
		Provider: "monitor",
		Instance: map[string]any{
			"backend": backend,
			"nodes":   nodeNames,
			"events":  cfg.Events,
			"extra":   cfg.Extra,
		},
	})
	fmt.Printf("[apply] monitor %s  backend=%s  nodes=%v\n", n.ID.Name, backend, nodeNames)
	return nil
}

func (e *Executor) destroyMonitor(r state.Resource) error {
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}
