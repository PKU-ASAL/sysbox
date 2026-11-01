package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
)

type Catalog struct {
	GcSourceID          string
	GcRepository        func(string) (*persistence.Repository, error)
	RunsDir             string
	WorkspacesDir       string
	stateBackend        string
	StateManager        func(topology string) (*state.Manager, error)
	health              controlstore.HealthStore
	PlacementStore      controlstore.PlacementStore
	LoadRuns            func(context.Context) ([]controlplane.Run, error)
	LoadNodeOperations  func(context.Context, string) ([]controlplane.NodeOperation, error)
	LoadGuestActivity   func(context.Context, string) ([]persistence.GuestActivity, error)
	LoadConsoleSessions func(context.Context, string) ([]controlplane.ConsoleSession, error)
	LoadAgentCommands   func(context.Context) ([]controlplane.AgentCommand, error)
	LockAdmission       func(string) func()
	DeleteState         func(context.Context, string, int64, string) error
}

type Info struct {
	ArtifactID       string                       `json:"artifact_id"`
	TopologyID       string                       `json:"topology_id,omitempty"`
	Name             string                       `json:"name"`
	HasHCL           bool                         `json:"has_hcl"`
	HasState         bool                         `json:"has_state"`
	ResourceCount    int                          `json:"resource_count,omitempty"`
	Serial           int64                        `json:"serial,omitempty"`
	Backend          string                       `json:"backend,omitempty"`
	Status           *controlplane.TopologyStatus `json:"status,omitempty"`
	ActiveRunID      string                       `json:"active_run_id,omitempty"`
	ActiveRevision   string                       `json:"active_revision,omitempty"`
	PlacementAgentID string                       `json:"placement_agent_id,omitempty"`
}

func New(runsDir, workspacesDir, stateBackend string, stateManager func(string) (*state.Manager, error), health controlstore.HealthStore) *Catalog {
	return &Catalog{
		RunsDir:       runsDir,
		WorkspacesDir: workspacesDir,
		stateBackend:  stateBackend,
		StateManager:  stateManager,
		health:        health,
	}
}

func (s *Catalog) HCLFile(topology string) string {
	base := filepath.Join(s.WorkspacesDir, topology)
	current := filepath.Join(base, ".current")
	if _, err := os.Lstat(current); os.IsNotExist(err) {
		// Manually supplied workspaces retain their existing layout. Published
		// workspaces always resolve the pointer once, before reading any files.
		return filepath.Join(base, "field.sysbox.hcl")
	}
	pinned, err := filepath.EvalSymlinks(current)
	if err != nil {
		// Preserve the failing pointer path so the caller's file read fails.
		// Never fall back to stale files after a publication pointer exists.
		return filepath.Join(current, "field.sysbox.hcl")
	}
	return filepath.Join(pinned, "field.sysbox.hcl")
}

func (s *Catalog) StateFile(topology string) string {
	return filepath.Join(s.RunsDir, topology, "state.json")
}

// UpsertProject publishes a complete immutable tree, then atomically switches
// the workspace pointer. Existing readers retain their pinned revision. Old
// revisions are retained for runs and recovery; publishing does not perform GC.
func (s *Catalog) UpsertProject(ctx context.Context, topology string, files map[string][]byte) error {
	finish, err := s.BeginGCProductionWrite(ctx, topology, "workspace")
	if err != nil {
		return err
	}
	if err = s.upsertProject(ctx, topology, files); err != nil {
		return err
	}
	return finish(map[string][]string{"revision": {controlplane.ComputeRevisionDigest(files)}})
}

func (s *Catalog) upsertProject(ctx context.Context, topology string, files map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return err
	}
	unlock, err := s.lockPublication(ctx, topology)
	if err != nil {
		return err
	}
	defer unlock()
	if intent, intentErr := s.ReadDeletionIntent(topology); intentErr != nil {
		return intentErr
	} else if intent != nil {
		return fmt.Errorf("%w: topology deletion is already in progress; retry after cleanup", persistence.ErrConflict)
	}
	hcl, err := s.materializeRevision(ctx, topology, controlplane.ComputeRevisionDigest(files), files)
	if err != nil {
		return err
	}
	base := filepath.Join(s.WorkspacesDir, topology)
	if err := mkdirWorkspaceDirectories(base); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	stage, err := os.MkdirTemp(base, ".publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	target, err := filepath.Abs(filepath.Dir(hcl))
	if err != nil {
		return err
	}
	link := filepath.Join(stage, "current")
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("stage workspace pointer: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(link, filepath.Join(base, ".current")); err != nil {
		return fmt.Errorf("publish workspace pointer: %w", err)
	}
	return syncWorkspaceParents(base)
}

func (s *Catalog) HCL(topology string) ([]byte, error) {
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.HCLFile(topology))
	if err != nil {
		return nil, fmt.Errorf("topology %q not found", topology)
	}
	return data, nil
}

func (s *Catalog) Get(ctx context.Context, topology string) (Info, error) {
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return Info{}, err
	}
	out := Info{ArtifactID: artifactID(topology), TopologyID: topologyID(topology), Name: topology}
	placement, err := s.LoadPlacement(topology)
	if err != nil {
		return Info{}, err
	}
	if placement != nil {
		out.PlacementAgentID = placement.AgentID
	}
	if _, err := os.Stat(s.HCLFile(topology)); err == nil {
		out.HasHCL = true
	}
	// Successful loading can initialize an empty aggregate. Catalog membership
	// distinguishes durable state/journal evidence from that empty anchor.
	mgr, err := s.StateManager(topology)
	if err != nil {
		return Info{}, err
	}
	listed, err := mgr.ListTopologies(ctx)
	if err != nil {
		return Info{}, err
	}
	for _, metadata := range listed {
		if metadata.Name == topology {
			out.HasState = metadata.HasState
			out.ResourceCount = metadata.ResourceCount
			out.Serial = metadata.Serial
			out.Backend = metadata.Backend
			break
		}
	}
	if s.LoadRuns != nil {
		runs, err := s.LoadRuns(ctx)
		if err != nil {
			return Info{}, err
		}
		for _, run := range controlstore.LatestRunsByID(runs) {
			if run.Topology == topology && run.Status.IsActive() {
				out.ActiveRunID, out.ActiveRevision = run.ID, run.Revision
			}
		}
	}
	return out, nil
}

func (s *Catalog) List(ctx context.Context) ([]Info, error) {
	items := map[string]*Info{}
	hclEntries, err := os.ReadDir(s.WorkspacesDir)
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range hclEntries {
		name := e.Name()
		if !e.IsDir() || controlstore.ValidatePathSegment(name, "topology") != nil {
			continue
		}
		if _, err := os.Stat(s.HCLFile(name)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("read workspace %s: %w", name, err)
		}
		items[name] = &Info{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name, HasHCL: true}
	}
	if s.stateBackend == "" {
		// Lifecycle aggregates are authoritative; standalone JSON is a retired
		// format and must not be used to infer that a managed topology is empty.
		aggregates, err := filepath.Glob(filepath.Join(s.RunsDir, "*", "state.json.lifecycle"))
		if err != nil {
			return nil, err
		}
		for _, dir := range aggregates {
			name := filepath.Base(filepath.Dir(dir))
			if controlstore.ValidatePathSegment(name, "topology") != nil {
				continue
			}
			mgr, err := s.StateManager(name)
			if err != nil {
				return nil, err
			}
			listed, err := mgr.ListTopologies(ctx)
			if err != nil {
				return nil, err
			}
			var current *state.TopologyMetadata
			for i := range listed {
				if listed[i].Name == name {
					current = &listed[i]
					break
				}
			}
			if current == nil {
				continue
			}
			info := items[name]
			if info == nil {
				info = &Info{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name}
				items[name] = info
			}
			info.HasState = current.HasState
			info.Serial = current.Serial
			info.ResourceCount = current.ResourceCount
			info.Backend = current.Backend
		}
		stateEntries, err := filepath.Glob(filepath.Join(s.RunsDir, "*", "state.json"))
		if err != nil {
			return nil, err
		}
		for _, e := range stateEntries {
			name := filepath.Base(filepath.Dir(e))
			info := items[name]
			if info == nil {
				info = &Info{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name}
				items[name] = info
			}
			info.HasState = true
			info.Backend = "local"
		}
	} else {
		repo, err := persistence.OpenCatalog(ctx, s.stateBackend)
		if err != nil {
			return nil, err
		}
		defer repo.Close()
		topologies, err := repo.ListTopologies(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range topologies {
			if err := controlstore.ValidatePathSegment(item.Name, "topology"); err != nil {
				continue
			}
			info := items[item.Name]
			if info == nil {
				info = &Info{ArtifactID: artifactID(item.Name), TopologyID: topologyID(item.Name), Name: item.Name}
				items[item.Name] = info
			}
			info.HasState = item.HasState
			info.ResourceCount = item.ResourceCount
			info.Serial = item.Serial
			info.Backend = item.Backend
		}
	}
	if s.LoadRuns != nil {
		runs, err := s.LoadRuns(ctx)
		if err != nil {
			return nil, err
		}
		placements := map[string]*controlstore.Placement{}
		for _, run := range controlstore.LatestRunsByID(runs) {
			if controlstore.ValidatePathSegment(run.Topology, "topology") != nil {
				continue
			}
			placement, loaded := placements[run.Topology]
			if !loaded {
				var err error
				placement, err = s.LoadPlacement(run.Topology)
				if err != nil {
					return nil, err
				}
				placements[run.Topology] = placement
			}
			placed := placement != nil && placement.AgentID == run.AgentID
			if !run.Status.IsActive() && !placed {
				continue
			}
			info := items[run.Topology]
			if info == nil {
				info = &Info{ArtifactID: artifactID(run.Topology), TopologyID: topologyID(run.Topology), Name: run.Topology}
				items[run.Topology] = info
			}
			if placed {
				info.PlacementAgentID = placement.AgentID
			}
			if run.Status.IsActive() {
				info.ActiveRunID, info.ActiveRevision = run.ID, run.Revision
			}
		}
	}
	out := make([]Info, 0, len(items))
	for _, info := range items {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Catalog) Delete(ctx context.Context, topology string, force bool) error {
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return err
	}
	if s.LockAdmission != nil {
		unlock := s.LockAdmission(topology)
		defer unlock()
	}
	var runs []controlplane.Run
	var nodeOperations []controlplane.NodeOperation
	if s.LoadRuns != nil {
		var err error
		runs, err = s.LoadRuns(ctx)
		if err != nil {
			return fmt.Errorf("read active operations: %w", err)
		}
		for _, run := range controlstore.LatestRunsByID(runs) {
			if run.Topology == topology && run.Status.IsActive() {
				return fmt.Errorf("%w: topology has an active run; metadata cannot be deleted", persistence.ErrConflict)
			}
		}
	}
	if s.LoadNodeOperations != nil {
		var err error
		nodeOperations, err = s.LoadNodeOperations(ctx, "")
		if err != nil {
			return fmt.Errorf("read active node operations: %w", err)
		}
		for _, op := range nodeOperations {
			if op.Topology == topology && op.Status != controlplane.NodeOperationStatusDone && op.Status != controlplane.NodeOperationStatusFailed {
				return fmt.Errorf("%w: topology has an active node operation; metadata cannot be deleted", persistence.ErrConflict)
			}
		}
	}
	var guestActivity []persistence.GuestActivity
	if s.LoadGuestActivity != nil {
		var err error
		guestActivity, err = s.LoadGuestActivity(ctx, topology)
		if err != nil {
			return err
		}
		for _, activity := range guestActivity {
			if activity.Unresolved() {
				return fmt.Errorf("%w: active guest operation blocks metadata deletion", persistence.ErrConflict)
			}
		}
	}
	if err := s.checkConsoleActivity(ctx, topology); err != nil {
		return err
	}
	if err := s.CheckCommandActivity(ctx, topology); err != nil {
		return err
	}
	placement, err := s.LoadPlacement(topology)
	if err != nil {
		return err
	}
	if placement != nil && !force {
		if s.LoadRuns == nil || s.LoadNodeOperations == nil {
			return fmt.Errorf("%w: cleanup evidence store unavailable", persistence.ErrConflict)
		}
		if receipt, reason := CurrentCleanupReceipt(topology, placement, runs, nodeOperations); receipt == nil {
			return fmt.Errorf("%w: owning-agent cleanup is not verified (%s); complete resource teardown on agent %q or use force=true only to explicitly discard metadata (remote resources are not removed)", persistence.ErrConflict, reason, placement.AgentID)
		} else if err := GuestActivityBeforeReceipt(guestActivity, receipt); err != nil {
			return err
		}
	}
	unlock, err := s.lockPublication(ctx, topology)
	if err != nil {
		return err
	}
	defer unlock()
	if intent, intentErr := s.ReadDeletionIntent(topology); intentErr != nil {
		return intentErr
	} else if intent != nil {
		return s.finishDeletionIntent(ctx, *intent)
	}
	mgr, err := s.StateManager(topology)
	if err != nil {
		return err
	}
	st, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("read topology state: %w", err)
	}
	if len(st.Resources) > 0 {
		if !force {
			return fmt.Errorf("topology %q has %d resource(s); call POST /v1/topologies/%s/destroy first or use force=true to delete metadata only", topology, len(st.Resources), topology)
		}
		slog.Warn("force-deleting topology metadata with live resources", "topology", topology, "resources", len(st.Resources))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.DeleteState == nil {
		return fmt.Errorf("%w: authority-aware state deletion unavailable", persistence.ErrConflict)
	}
	if err := s.WriteDeletionIntent(DeletionIntent{Topology: topology, UID: st.TopologyUID, Serial: st.Meta.Serial, Created: time.Now().UTC()}); err != nil {
		return fmt.Errorf("persist deletion intent: %w", err)
	}
	if err := s.DeleteState(ctx, topology, st.Meta.Serial, st.TopologyUID); err != nil {
		return fmt.Errorf("remove state: %w", err)
	}
	if s.stateBackend == "" {
		if err := s.removeWorkspaceMetadata(topology); err != nil {
			return fmt.Errorf("remove state: %w", err)
		}
	}
	if err := os.RemoveAll(filepath.Join(s.WorkspacesDir, topology)); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	if _, err := os.Stat(s.WorkspacesDir); err == nil {
		if err := syncWorkspaceParents(s.WorkspacesDir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := s.deletePlacementUnpublished(ctx, topology); err != nil {
		return fmt.Errorf("remove topology placement: %w", err)
	}
	if s.health != nil {
		_ = s.health.DeleteHealth(ctx, topology)
	}
	if err := os.Remove(s.DeletionIntentPath(topology)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Metadata deletion is not immutable-tree garbage collection. A run or reader
// may still be using a pinned revision or snapshot after the workspace unlinks.
func (s *Catalog) removeWorkspaceMetadata(topology string) error {
	dir := filepath.Dir(s.StateFile(topology))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// The aggregate retains its fresh owner identity and stable flock inode.
		// Removing it could split concurrent writers across two different locks.
		if entry.Name() == "revisions" || entry.Name() == "snapshots" || entry.Name() == filepath.Base(s.StateFile(topology))+".lifecycle" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return syncWorkspaceParents(dir)
}

func (s *Catalog) LoadState(topology string) (*state.State, error) {
	mgr, err := s.StateManager(topology)
	if err != nil {
		return nil, err
	}
	st, err := mgr.Load()
	if err != nil {
		return nil, err
	}
	if s.stateBackend == "" && len(st.Resources) == 0 && st.Meta.Serial == 0 {
		listed, err := mgr.ListTopologies(context.Background())
		if err != nil {
			return nil, err
		}
		for _, item := range listed {
			if item.Name == topology && item.HasState {
				return st, nil
			}
		}
		if _, err := os.Stat(s.StateFile(topology)); err != nil {
			return nil, fmt.Errorf("topology %q: no state file", topology)
		}
	}
	return st, nil
}

func (s *Catalog) Names(ctx context.Context) ([]string, error) {
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
		if controlstore.ValidatePathSegment(name, "topology") == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Catalog) Metadata(ctx context.Context, topology string) (state.Metadata, error) {
	mgr, err := s.StateManager(topology)
	if err != nil {
		return state.Metadata{}, err
	}
	return mgr.Metadata(ctx)
}

func (s *Catalog) LockInfo(ctx context.Context, topology string) (state.LockInfo, error) {
	mgr, err := s.StateManager(topology)
	if err != nil {
		return state.LockInfo{}, err
	}
	return mgr.LockInfo(ctx)
}

func (s *Catalog) ForceUnlock(ctx context.Context, topology string) error {
	if s.LockAdmission != nil {
		unlock := s.LockAdmission(topology)
		defer unlock()
	}
	if err := s.EnsureTopologyQuiescent(ctx, topology); err != nil {
		return err
	}
	mgr, err := s.StateManager(topology)
	if err != nil {
		return err
	}
	info, err := mgr.LockInfo(ctx)
	if err != nil {
		return err
	}
	if info.Locked {
		return fmt.Errorf("%w: state lease is still live; wait for expiry before force-unlock", persistence.ErrConflict)
	}
	return mgr.ForceUnlock(ctx)
}

// ensureTopologyQuiescent is the shared safety gate for destructive state
// maintenance. A backend lock is only a storage mutex; removing it while an
// effect holder is alive would create two writers and invalidate fencing.
func (s *Catalog) EnsureTopologyQuiescent(ctx context.Context, topology string) error {
	if s.LoadRuns == nil || s.LoadNodeOperations == nil || s.LoadGuestActivity == nil || s.LoadConsoleSessions == nil || s.LoadAgentCommands == nil {
		return fmt.Errorf("%w: complete activity registry required for state maintenance", persistence.ErrConflict)
	}
	if s.LoadRuns != nil {
		runs, err := s.LoadRuns(ctx)
		if err != nil {
			return err
		}
		for _, run := range controlstore.LatestRunsByID(runs) {
			if run.Topology == topology && controlstore.RunActivityUnresolved(run) {
				return fmt.Errorf("%w: active run prevents state maintenance", persistence.ErrConflict)
			}
		}
	}
	if s.LoadNodeOperations != nil {
		// This store filters by workspace, not topology. A topology can live in
		// a differently named workspace; enumerate first and match Topology below.
		ops, err := s.LoadNodeOperations(ctx, "")
		if err != nil {
			return err
		}
		for _, op := range ops {
			if op.Topology == topology && persistence.NodeOperationUnresolved(op) {
				return fmt.Errorf("%w: active node operation prevents state maintenance", persistence.ErrConflict)
			}
		}
	}
	if s.LoadGuestActivity != nil {
		activities, err := s.LoadGuestActivity(ctx, topology)
		if err != nil {
			return err
		}
		for _, activity := range activities {
			if activity.Unresolved() {
				return fmt.Errorf("%w: active guest operation prevents state maintenance", persistence.ErrConflict)
			}
		}
	}
	if err := s.checkConsoleActivity(ctx, topology); err != nil {
		return err
	}
	return s.CheckCommandActivity(ctx, topology)
}

func (s *Catalog) StateSnapshots(ctx context.Context, topology string) ([]state.Snapshot, error) {
	mgr, err := s.StateManager(topology)
	if err != nil {
		return nil, err
	}
	snapshots, ok := mgr.Backend().(state.SnapshotBackend)
	if !ok {
		return nil, state.ErrUnsupported
	}
	return snapshots.ListSnapshots(ctx)
}
