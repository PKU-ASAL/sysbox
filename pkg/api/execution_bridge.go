package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// ExecutionBridge opens only execution storage and immutable workspaces. It has
// no HTTP server, scheduler, console registry, issuer key or canonical completion
// writer. Only the privileged host opens this execution environment.
type ExecutionBridge struct {
	lifecycleRepositories
	cfg        config.ServiceConfig
	store      apiStore
	workspaces *WorkspaceService
	logs       *RunLogHub
	identityMu sync.RWMutex
	hostID     string
	hostKeys   map[string]ed25519.PublicKey
}

func NewExecutionBridge(cfg config.ServiceConfig) (*ExecutionBridge, error) {
	if cfg.Paths.RunsDir == "" {
		cfg.Paths.RunsDir = config.DefaultRunsDir()
	}
	if cfg.Paths.WorkspacesDir == "" {
		cfg.Paths.WorkspacesDir = config.DefaultWorkspacesDir()
	}
	store, _, err := openExecutionStorage(cfg, cfg.Paths.RunsDir, cfg.State.Backend)
	if err != nil {
		return nil, err
	}
	b := &ExecutionBridge{cfg: cfg, store: store, logs: newRunLogHub(), hostKeys: map[string]ed25519.PublicKey{}}
	success := false
	defer func() {
		if !success {
			_ = b.Close()
		}
	}()
	// Opening SQL validates its actual schema even though no Jobs/Console service
	// loads records. Corrupt storage cannot silently become a fresh local store.
	switch s := store.(type) {
	case *sqliteAPIStore:
		_, err = s.open()
	case *postgresAPIStore:
		_, err = s.poolFor(context.Background())
	}
	if err != nil {
		return nil, err
	}
	for id, path := range cfg.API.ExecutionGrant.HostPublicKeyFiles {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("load execution host public key: %w", readErr)
		}
		if id == "" || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid execution host public key")
		}
		b.hostKeys[id] = append(ed25519.PublicKey(nil), raw...)
	}
	b.workspaces = newWorkspaceService(cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir, cfg.State.Backend, b.StateManager, store)
	b.workspaces.gcSourceID = cfg.GCPublicationSource
	b.workspaces.gcRepository = b.actionRepository
	b.workspaces.loadRuns = store.LoadRuns
	if p, ok := store.(durablePlacementStore); ok {
		b.workspaces.placementStore = p
	}
	success = true
	return b, nil
}

func (b *ExecutionBridge) BindHostIdentity(id string, key ed25519.PublicKey) error {
	if b == nil || b.store == nil || id == "" || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("host identity and public key required")
	}
	b.identityMu.Lock()
	defer b.identityMu.Unlock()
	if b.hostID != "" && (b.hostID != id || !bytes.Equal(b.hostKeys[id], key)) {
		return fmt.Errorf("host identity cannot be rebound")
	}
	if prior := b.hostKeys[id]; len(prior) > 0 && !bytes.Equal(prior, key) {
		return fmt.Errorf("configured host public key conflicts")
	}
	b.hostID = id
	b.hostKeys[id] = append(ed25519.PublicKey(nil), key...)
	return nil
}
func (b *ExecutionBridge) hostKey(id string) ed25519.PublicKey {
	b.identityMu.RLock()
	defer b.identityMu.RUnlock()
	return append(ed25519.PublicKey(nil), b.hostKeys[id]...)
}
func (b *ExecutionBridge) AttachRun(run *controlplane.Run) {
	if run != nil {
		b.logs.Ensure(run.ID, false)
	}
}
func (b *ExecutionBridge) LogWriter(id string) io.Writer { return b.logs.Writer(id) }
func (b *ExecutionBridge) LockTopology(ctx context.Context, topology string) (func(), error) {
	repo, err := b.actionRepository(topology)
	if err != nil {
		return nil, err
	}
	return repo.LockExecution(ctx, topology)
}
func (b *ExecutionBridge) Finish(run *controlplane.Run, err error) {
	if run == nil {
		return
	}
	run.MarkFinished(err, time.Now())
	run.Inputs = nil
	b.logs.Close(run.ID)
	// The API commits canonical completion under ownership CAS/signed host proof.
}
func (b *ExecutionBridge) actionRepository(topology string) (*persistence.Repository, error) {
	return b.lifecycleRepositories.open(b.cfg.Paths.RunsDir, b.cfg.State.Backend, topology)
}
func (b *ExecutionBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	return b.actionRepository(topology)
}
func (b *ExecutionBridge) StateManager(topology string) (*state.Manager, error) {
	repo, err := b.actionRepository(topology)
	if err != nil {
		return nil, err
	}
	return state.NewManagerWithBackend(repo.StateBackend(topology)), nil
}
func (b *ExecutionBridge) HCLFile(topology string) string { return b.workspaces.HCLFile(topology) }
func (b *ExecutionBridge) HCLFileForRun(run *controlplane.Run) string {
	if run != nil {
		return run.SnapshotPath
	}
	return ""
}
func (b *ExecutionBridge) MaterializeRunRevision(ctx context.Context, topology, revision string) (string, error) {
	if b == nil || b.store == nil || topology == "" || revision == "" {
		return "", fmt.Errorf("immutable run revision is required")
	}
	value, err := b.store.GetGlobalRevision(ctx, revision)
	if err != nil {
		return "", err
	}
	return b.workspaces.MaterializeRevision(topology, revision, value.Files)
}
func (b *ExecutionBridge) Topologies(ctx context.Context) []string {
	items, err := b.workspaces.List(ctx)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, item := range items {
		if item.HasState {
			out = append(out, item.Name)
		}
	}
	return out
}

func (b *ExecutionBridge) ObservationTopologies(ctx context.Context) ([]string, error) {
	b.identityMu.RLock()
	id := b.hostID
	b.identityMu.RUnlock()
	if id == "" {
		return nil, fmt.Errorf("host identity required for observation")
	}
	items, err := b.workspaces.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, item := range items {
		if !item.HasState {
			continue
		}
		placement, err := b.workspaces.loadPlacementContext(ctx, item.Name)
		if errors.Is(err, persistence.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if placement != nil && placement.AgentID == id {
			names = append(names, item.Name)
		}
	}
	return names, nil
}
func (b *ExecutionBridge) CheckpointFile(topology, id string) string {
	return filepath.Join(b.cfg.Paths.RunsDir, topology, "runs", id+".checkpoint.json")
}
func (b *ExecutionBridge) CheckpointStore() runtime.CheckpointStore { return b.store }
func (b *ExecutionBridge) ParentRun(ctx context.Context, id string) (*controlplane.Run, error) {
	return b.store.GetRun(ctx, id)
}
func (b *ExecutionBridge) Preflight(ctx context.Context, run *controlplane.Run, log io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := agentexec.RunWorkspace(ctx, run)
	if path == "" {
		return fmt.Errorf("run preflight requires an immutable snapshot")
	}
	result, err := preflightFile(path)
	if err != nil {
		return err
	}
	writePreflightLogsTo(log, result)
	return result.err()
}
func (b *ExecutionBridge) BeforeReset(*runtime.Plan) error { return nil }

// Close is called after worker/supervisor handlers have stopped.
func (b *ExecutionBridge) Close() error {
	failures := []error{b.lifecycleRepositories.close()}
	switch s := b.store.(type) {
	case *sqliteAPIStore:
		s.openMu.Lock()
		if s.db != nil {
			failures = append(failures, s.db.Close())
			s.db = nil
		}
		s.openMu.Unlock()
	case *postgresAPIStore:
		s.mu.Lock()
		if s.pool != nil {
			s.pool.Close()
			s.pool = nil
		}
		s.mu.Unlock()
	}
	return errors.Join(failures...)
}
