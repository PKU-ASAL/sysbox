package agentexec

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

func Observe(ctx context.Context, agentID string, bridge Bridge) []controlplane.ResourceProjection {
	if bridge == nil {
		return nil
	}
	topologies := bridge.Topologies(ctx)
	out := make([]controlplane.ResourceProjection, 0, len(topologies))
	for _, topology := range topologies {
		mgr, err := bridge.StateManager(topology)
		if err != nil {
			continue
		}
		st, err := mgr.Load()
		if err != nil || st == nil {
			continue
		}
		meta, _ := mgr.Metadata(ctx)
		health := runtime.EvaluateTopologyHealth(ctx, st)
		out = append(out, controlplane.ResourceProjection{
			AgentID:    agentID,
			Workspace:  topology,
			Topology:   topology,
			Serial:     meta.Serial,
			ObservedAt: time.Now().UTC(),
			Health:     health,
			Resources:  health.Resources,
		})
	}
	return out
}

func topologiesFromRunsDir(runsDir string) []string {
	entries, _ := filepath.Glob(filepath.Join(runsDir, "*", "state.json"))
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := filepath.Base(filepath.Dir(entry))
		if name != "" && !strings.HasPrefix(name, ".") {
			out = append(out, name)
		}
	}
	return out
}
