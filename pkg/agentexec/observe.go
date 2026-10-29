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
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type HostObservationOptions struct {
	Capabilities  []string
	ID            string
	Labels        map[string]string
	CapacityPath  string
	ArtifactPaths []string
}

type ObservationSource interface {
	ObservationTopologies(context.Context) ([]string, error)
	StateManager(string) (*state.Manager, error)
	HCLFile(string) string
}

// CollectHostObservation is wired only inside the privileged host service.
// Failures preserve uncertainty instead of publishing an empty fresh inventory.
func CollectHostObservation(ctx context.Context, opts HostObservationOptions, source ObservationSource) (controlplane.AgentInventory, []controlplane.ResourceProjection, error) {
	topologies, err := source.ObservationTopologies(ctx)
	if err != nil {
		return controlplane.AgentInventory{}, nil, err
	}
	projections := make([]controlplane.ResourceProjection, 0, len(topologies))
	items := make([]controlplane.InventoryItem, 0, len(topologies))
	for _, name := range topologies {
		p, err := observeTopology(ctx, opts.ID, source, name)
		if err != nil {
			return controlplane.AgentInventory{}, nil, err
		}
		projections = append(projections, p)
		items = append(items, controlplane.InventoryItem{Workspace: p.Workspace, Topology: p.Topology, Serial: p.Serial, ResourceCount: len(p.Resources), Health: string(p.Health.Status), Available: localWorkspaceAvailable(source.HCLFile(name))})
	}
	resources, artifacts := hostInventory(opts.CapacityPath, opts.ArtifactPaths)
	return controlplane.AgentInventory{AgentID: opts.ID, Capabilities: append([]string(nil), opts.Capabilities...), Labels: opts.Labels, Resources: resources, Topologies: items, Artifacts: artifacts, ObservedAt: time.Now().UTC()}, projections, nil
}

// Timestamp the snapshot before probing: a slow older probe must not outrank
// the completion snapshot. Serial and resources come from the same state read.
func observeTopology(ctx context.Context, agentID string, bridge interface {
	StateManager(string) (*state.Manager, error)
}, topology string) (controlplane.ResourceProjection, error) {
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
	health := runtime.EvaluateTopologyHealth(ctx, st, driver.DefaultRegistry)
	if err := ctx.Err(); err != nil {
		return controlplane.ResourceProjection{}, err
	}
	return controlplane.ResourceProjection{
		AgentID: agentID, Workspace: topology, Topology: topology,
		Serial: st.Meta.Serial, ObservedAt: observedAt,
		Health: health, Resources: health.Resources,
	}, nil
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
