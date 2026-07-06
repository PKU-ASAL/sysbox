package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/state"
)

type WorkspaceService struct {
	runsDir       string
	workspacesDir string
	stateBackend  string
	stateManager  func(topology string) (*state.Manager, error)
}

type WorkspaceInfo struct {
	ArtifactID    string `json:"artifact_id"`
	TopologyID    string `json:"topology_id,omitempty"`
	Name          string `json:"name"`
	HasHCL        bool   `json:"has_hcl"`
	HasState      bool   `json:"has_state"`
	ResourceCount int    `json:"resource_count,omitempty"`
	Serial        int64  `json:"serial,omitempty"`
	Backend       string `json:"backend,omitempty"`
}

func newWorkspaceService(runsDir, workspacesDir, stateBackend string, stateManager func(string) (*state.Manager, error)) *WorkspaceService {
	return &WorkspaceService{
		runsDir:       runsDir,
		workspacesDir: workspacesDir,
		stateBackend:  stateBackend,
		stateManager:  stateManager,
	}
}

func (s *WorkspaceService) HCLFile(topology string) string {
	return filepath.Join(s.workspacesDir, topology, "field.sysbox.hcl")
}

func (s *WorkspaceService) StateFile(topology string) string {
	return filepath.Join(s.runsDir, topology, "state.json")
}

func (s *WorkspaceService) Create(ctx context.Context, name, hcl string) (WorkspaceInfo, error) {
	if err := validatePathSegment(name, "name"); err != nil {
		return WorkspaceInfo{}, err
	}
	if hcl == "" {
		return WorkspaceInfo{}, fmt.Errorf("hcl is required")
	}
	if _, err := config.ParseString(hcl, ".hcl"); err != nil {
		return WorkspaceInfo{}, fmt.Errorf("invalid HCL: %w", err)
	}
	hclPath := s.HCLFile(name)
	if _, err := os.Stat(hclPath); err == nil {
		return WorkspaceInfo{}, fmt.Errorf("topology %q already exists", name)
	}
	if err := os.MkdirAll(filepath.Dir(hclPath), 0o755); err != nil {
		return WorkspaceInfo{}, fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(hclPath, []byte(hcl), 0o644); err != nil {
		return WorkspaceInfo{}, fmt.Errorf("write hcl: %w", err)
	}
	return WorkspaceInfo{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name, HasHCL: true}, nil
}

func (s *WorkspaceService) UpdateHCL(ctx context.Context, topology string, hcl []byte) error {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return err
	}
	if len(hcl) == 0 {
		return fmt.Errorf("empty HCL")
	}
	hclPath := s.HCLFile(topology)
	if _, err := os.Stat(hclPath); err != nil {
		return fmt.Errorf("topology %q not found", topology)
	}
	if _, err := config.ParseString(string(hcl), ".hcl"); err != nil {
		return fmt.Errorf("invalid HCL: %w", err)
	}
	if err := os.WriteFile(hclPath, hcl, 0o644); err != nil {
		return fmt.Errorf("write hcl: %w", err)
	}
	return nil
}

func (s *WorkspaceService) HCL(topology string) ([]byte, error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.HCLFile(topology))
	if err != nil {
		return nil, fmt.Errorf("topology %q not found", topology)
	}
	return data, nil
}

func (s *WorkspaceService) Get(ctx context.Context, topology string) (WorkspaceInfo, error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return WorkspaceInfo{}, err
	}
	out := WorkspaceInfo{ArtifactID: artifactID(topology), TopologyID: topologyID(topology), Name: topology}
	if _, err := os.Stat(s.HCLFile(topology)); err == nil {
		out.HasHCL = true
	}
	if st, err := s.LoadState(topology); err == nil {
		out.HasState = true
		out.ResourceCount = len(st.Resources)
	}
	return out, nil
}

func (s *WorkspaceService) List(ctx context.Context) ([]WorkspaceInfo, error) {
	items := map[string]*WorkspaceInfo{}
	hclEntries, err := filepath.Glob(filepath.Join(s.workspacesDir, "*", "field.sysbox.hcl"))
	if err != nil {
		return nil, err
	}
	for _, e := range hclEntries {
		name := filepath.Base(filepath.Dir(e))
		items[name] = &WorkspaceInfo{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name, HasHCL: true}
	}
	if s.stateBackend == "" {
		stateEntries, err := filepath.Glob(filepath.Join(s.runsDir, "*", "state.json"))
		if err != nil {
			return nil, err
		}
		for _, e := range stateEntries {
			name := filepath.Base(filepath.Dir(e))
			info := items[name]
			if info == nil {
				info = &WorkspaceInfo{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name}
				items[name] = info
			}
			info.HasState = true
			info.Backend = "local"
		}
	} else {
		mgr, err := s.stateManager("__list__")
		if err != nil {
			return nil, err
		}
		topologies, err := mgr.ListTopologies(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range topologies {
			if err := validatePathSegment(item.Name, "topology"); err != nil {
				continue
			}
			info := items[item.Name]
			if info == nil {
				info = &WorkspaceInfo{ArtifactID: artifactID(item.Name), TopologyID: topologyID(item.Name), Name: item.Name}
				items[item.Name] = info
			}
			info.HasState = item.HasState
			info.ResourceCount = item.ResourceCount
			info.Serial = item.Serial
			info.Backend = item.Backend
		}
	}
	out := make([]WorkspaceInfo, 0, len(items))
	for _, info := range items {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *WorkspaceService) Delete(ctx context.Context, topology string, force bool) error {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return err
	}
	mgr, err := s.stateManager(topology)
	if err != nil {
		return err
	}
	st, err := mgr.Load()
	if err == nil && len(st.Resources) > 0 {
		if !force {
			return fmt.Errorf("topology %q has %d resource(s); call POST /v1/topologies/%s/destroy first or use force=true to delete metadata only", topology, len(st.Resources), topology)
		}
		slog.Warn("force-deleting topology metadata with live resources", "topology", topology, "resources", len(st.Resources))
	}
	if s.stateBackend == "" {
		if err := os.RemoveAll(filepath.Dir(s.StateFile(topology))); err != nil {
			return fmt.Errorf("remove state: %w", err)
		}
	} else if err := mgr.Delete(ctx); err != nil {
		return fmt.Errorf("remove state: %w", err)
	}
	if err := os.RemoveAll(filepath.Dir(s.HCLFile(topology))); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	return nil
}

func (s *WorkspaceService) LoadState(topology string) (*state.State, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return nil, err
	}
	st, err := mgr.Load()
	if err != nil {
		return nil, err
	}
	if s.stateBackend == "" && len(st.Resources) == 0 {
		if _, err := os.Stat(s.StateFile(topology)); err != nil {
			return nil, fmt.Errorf("topology %q: no state file", topology)
		}
	}
	return st, nil
}

func (s *WorkspaceService) Names(ctx context.Context) ([]string, error) {
	names := map[string]bool{}
	workspaces, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, info := range workspaces {
		names[info.Name] = true
	}
	out := make([]string, 0, len(names))
	for name := range names {
		if validatePathSegment(name, "topology") == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *WorkspaceService) Metadata(ctx context.Context, topology string) (state.Metadata, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return state.Metadata{}, err
	}
	return mgr.Metadata(ctx)
}

func (s *WorkspaceService) LockInfo(ctx context.Context, topology string) (state.LockInfo, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return state.LockInfo{}, err
	}
	return mgr.LockInfo(ctx)
}

func (s *WorkspaceService) ForceUnlock(ctx context.Context, topology string) error {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return err
	}
	return mgr.ForceUnlock(ctx)
}

func (s *WorkspaceService) StateSnapshots(ctx context.Context, topology string) ([]state.Snapshot, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return nil, err
	}
	snapshots, ok := mgr.Backend().(state.SnapshotBackend)
	if !ok {
		return []state.Snapshot{}, nil
	}
	return snapshots.ListSnapshots(ctx)
}

func (s *WorkspaceService) RestoreStateSnapshot(ctx context.Context, topology, snapshot string) error {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return err
	}
	snapshots, ok := mgr.Backend().(state.SnapshotBackend)
	if !ok {
		return fmt.Errorf("state backend does not support snapshots")
	}
	return snapshots.RestoreSnapshot(ctx, snapshot)
}
