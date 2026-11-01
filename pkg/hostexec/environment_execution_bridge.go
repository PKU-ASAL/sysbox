package hostexec

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

	"github.com/oslab/sysbox/pkg/driver"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/eventstream"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/workspace"
)

// Environment opens only execution storage and immutable workspaces. It has
// no HTTP server, scheduler, console registry, issuer key or canonical completion
// writer. Only the privileged host opens this execution environment.
type Environment struct {
	controlstore.LifecycleRepositories
	Cfg        config.ServiceConfig
	Store      controlstore.Store
	Workspaces *workspace.Catalog
	Logs       *eventstream.RunLogHub
	identityMu sync.RWMutex
	hostID     string
	HostKeys   map[string]ed25519.PublicKey
}

func Open(cfg config.ServiceConfig) (*Environment, error) {
	if cfg.Paths.RunsDir == "" {
		cfg.Paths.RunsDir = config.DefaultRunsDir()
	}
	if cfg.Paths.WorkspacesDir == "" {
		cfg.Paths.WorkspacesDir = config.DefaultWorkspacesDir()
	}
	store, _, err := controlstore.Open(cfg, cfg.Paths.RunsDir, cfg.State.Backend)
	if err != nil {
		return nil, err
	}
	b := &Environment{Cfg: cfg, Store: store, Logs: eventstream.NewRuns(), HostKeys: map[string]ed25519.PublicKey{}}
	success := false
	defer func() {
		if !success {
			_ = b.Close()
		}
	}()
	// Opening SQL validates its actual schema even though no Jobs/Console service
	// loads records. Corrupt storage cannot silently become a fresh local store.
	if err := controlstore.Validate(context.Background(), store); err != nil {
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
		b.HostKeys[id] = append(ed25519.PublicKey(nil), raw...)
	}
	b.Workspaces = workspace.New(cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir, cfg.State.Backend, b.StateManager, store)
	b.Workspaces.GcSourceID = cfg.GCPublicationSource
	b.Workspaces.GcRepository = b.Repository
	b.Workspaces.LoadRuns = store.LoadRuns
	if p, ok := store.(controlstore.PlacementStore); ok {
		b.Workspaces.PlacementStore = p
	}
	success = true
	return b, nil
}

func (b *Environment) BindHostIdentity(id string, key ed25519.PublicKey) error {
	if b == nil || b.Store == nil || id == "" || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("host identity and public key required")
	}
	b.identityMu.Lock()
	defer b.identityMu.Unlock()
	if b.hostID != "" && (b.hostID != id || !bytes.Equal(b.HostKeys[id], key)) {
		return fmt.Errorf("host identity cannot be rebound")
	}
	if prior := b.HostKeys[id]; len(prior) > 0 && !bytes.Equal(prior, key) {
		return fmt.Errorf("configured host public key conflicts")
	}
	b.hostID = id
	b.HostKeys[id] = append(ed25519.PublicKey(nil), key...)
	return nil
}

func (b *Environment) hostKey(id string) ed25519.PublicKey {
	b.identityMu.RLock()
	defer b.identityMu.RUnlock()
	return append(ed25519.PublicKey(nil), b.HostKeys[id]...)
}

func (b *Environment) AttachRun(run *controlplane.Run) {
	if run != nil {
		b.Logs.Ensure(run.ID, false)
	}
}

func (b *Environment) LogWriter(id string) io.Writer { return b.Logs.Writer(id) }

func (b *Environment) LockTopology(ctx context.Context, topology string) (func(), error) {
	repo, err := b.Repository(topology)
	if err != nil {
		return nil, err
	}
	return repo.LockExecution(ctx, topology)
}

func (b *Environment) Finish(run *controlplane.Run, err error) {
	if run == nil {
		return
	}
	run.MarkFinished(err, time.Now())
	run.Inputs = nil
	b.Logs.Close(run.ID)
	// The API commits canonical completion under ownership CAS/signed host proof.
}

func (b *Environment) Repository(topology string) (*persistence.Repository, error) {
	return b.LifecycleRepositories.Open(b.Cfg.Paths.RunsDir, b.Cfg.State.Backend, topology)
}

func (b *Environment) ActionRepository(topology string) (runtime.ActionRepository, error) {
	return b.Repository(topology)
}

func (b *Environment) StateManager(topology string) (*state.Manager, error) {
	repo, err := b.Repository(topology)
	if err != nil {
		return nil, err
	}
	return state.NewManagerWithBackend(repo.StateBackend(topology)), nil
}

func (b *Environment) HCLFile(topology string) string { return b.Workspaces.HCLFile(topology) }

func (b *Environment) HCLFileForRun(run *controlplane.Run) string {
	if run != nil {
		return run.SnapshotPath
	}
	return ""
}

func (b *Environment) MaterializeRunRevision(ctx context.Context, topology, revision string) (string, error) {
	if b == nil || b.Store == nil || topology == "" || revision == "" {
		return "", fmt.Errorf("immutable run revision is required")
	}
	value, err := b.Store.GetGlobalRevision(ctx, revision)
	if err != nil {
		return "", err
	}
	return b.Workspaces.MaterializeRevision(topology, revision, value.Files)
}

func (b *Environment) Topologies(ctx context.Context) []string {
	items, err := b.Workspaces.List(ctx)
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

func (b *Environment) ObservationTopologies(ctx context.Context) ([]string, error) {
	b.identityMu.RLock()
	id := b.hostID
	b.identityMu.RUnlock()
	if id == "" {
		return nil, fmt.Errorf("host identity required for observation")
	}
	items, err := b.Workspaces.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, item := range items {
		if !item.HasState {
			continue
		}
		placement, err := b.Workspaces.LoadPlacementContext(ctx, item.Name)
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

func (b *Environment) CheckpointFile(topology, id string) string {
	return filepath.Join(b.Cfg.Paths.RunsDir, topology, "runs", id+".checkpoint.json")
}

func (b *Environment) CheckpointStore() runtime.CheckpointStore { return b.Store }

func (b *Environment) ParentRun(ctx context.Context, id string) (*controlplane.Run, error) {
	return b.Store.GetRun(ctx, id)
}

func (b *Environment) Preflight(ctx context.Context, run *controlplane.Run, log io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := RunWorkspace(ctx, run)
	if path == "" {
		return fmt.Errorf("run preflight requires an immutable snapshot")
	}
	result, err := runtime.PreflightFile(path, driver.DefaultRegistry)
	if err != nil {
		return err
	}
	runtime.WritePreflightLogs(log, result)
	return result.Err()
}

func (b *Environment) BeforeReset(*runtime.Plan) error { return nil }

// Close is called after worker/supervisor handlers have stopped.
func (b *Environment) Close() error {
	return errors.Join(b.LifecycleRepositories.Close(), controlstore.Close(b.Store))
}
