package agentexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
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
		if stateHasFirecracker(st) {
			projection, err := observeTopology(ctx, agentID, bridge, topology)
			if err == nil {
				out = append(out, projection)
			}
			continue
		}
		// Preserve the original Docker observation path and its state semantics.
		meta, _ := mgr.Metadata(ctx)
		health := runtime.EvaluateTopologyHealth(ctx, st)
		out = append(out, controlplane.ResourceProjection{
			AgentID: agentID, Workspace: topology, Topology: topology,
			Serial: meta.Serial, ObservedAt: time.Now().UTC(),
			Health: health, Resources: health.Resources,
		})
	}
	return out
}

func stateHasFirecracker(st *state.State) bool {
	if st == nil {
		return false
	}
	for _, resource := range st.Resources {
		if resource.Driver == "firecracker" {
			return true
		}
	}
	return false
}

// Timestamp the snapshot before probing: a slow older probe must not outrank
// the completion snapshot. Serial and resources come from the same state read.
func observeTopology(ctx context.Context, agentID string, bridge Bridge, topology string) (controlplane.ResourceProjection, error) {
	observedAt := time.Now().UTC()
	mgr, err := bridge.StateManager(topology)
	if err != nil {
		return controlplane.ResourceProjection{}, err
	}
	st, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return controlplane.ResourceProjection{}, err
	}
	if st == nil {
		return controlplane.ResourceProjection{}, fmt.Errorf("state unavailable for topology %q", topology)
	}
	health := runtime.EvaluateTopologyHealth(ctx, st)
	if err := ctx.Err(); err != nil {
		return controlplane.ResourceProjection{}, err
	}
	return controlplane.ResourceProjection{
		AgentID: agentID, Workspace: topology, Topology: topology,
		Serial: st.Meta.Serial, ObservedAt: observedAt,
		Health: health, Resources: health.Resources,
	}, nil
}

func Inventory(ctx context.Context, opts Options, bridge Bridge) controlplane.AgentInventory {
	projections := Observe(ctx, opts.ID, bridge)
	items := make([]controlplane.InventoryItem, 0, len(projections))
	for _, proj := range projections {
		items = append(items, controlplane.InventoryItem{
			Workspace:     proj.Workspace,
			Topology:      proj.Topology,
			Serial:        proj.Serial,
			ResourceCount: len(proj.Resources),
			Health:        string(proj.Health.Status),
			Available:     localWorkspaceAvailable(bridge.HCLFile(proj.Topology)),
		})
	}
	resources, artifacts := hostInventory(opts.CapacityPath, opts.ArtifactPaths)
	return controlplane.AgentInventory{
		AgentID:      opts.ID,
		Capabilities: append([]string{}, opts.Capabilities...),
		Labels:       opts.Labels,
		Resources:    resources,
		Topologies:   items,
		Artifacts:    artifacts,
		ObservedAt:   time.Now().UTC(),
	}
}

func hostInventory(capacityPath string, artifactPaths []string) (controlplane.HostResourceInventory, []controlplane.InventoryItem) {
	resources := controlplane.HostResourceInventory{CPU: int64(goruntime.NumCPU())}
	var info syscall.Sysinfo_t
	if syscall.Sysinfo(&info) == nil {
		resources.MemoryBytes = int64(info.Totalram) * int64(info.Unit)
	}
	if capacityPath == "" {
		capacityPath = "."
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(capacityPath, &fs) == nil {
		resources.DiskBytes = int64(fs.Bavail) * int64(fs.Bsize)
	}
	artifacts := make([]controlplane.InventoryItem, 0, len(artifactPaths))
	for _, path := range artifactPaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		item := controlplane.InventoryItem{Kind: "artifact"}
		if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() {
			item.Available = true
			resources.ArtifactBytes += stat.Size()
		}
		artifacts = append(artifacts, item)
	}
	return resources, artifacts
}

func localWorkspaceAvailable(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
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
