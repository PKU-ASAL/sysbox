package agentexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type LocalOptions struct {
	Topology         string
	ConfigFile       string
	StatePath        string
	BackendURL       string
	AllowUnsafeState bool
	RunsDir          string
	Log              io.Writer
	Refresh          bool
	Target           string
	BeforeApply      func(*runtime.Plan) error
	BeforeDestroy    func(*runtime.Plan) error
	BeforeReset      func(*runtime.Plan) error
	AfterRun         func(*controlplane.Run)
	// PrepareExecution is a credential-owning local controller hook. It runs
	// after the topology lock is acquired and before any runtime provider call.
	PrepareExecution func(context.Context, *controlplane.Run) (context.Context, func(), error)
	CheckpointStore  runtime.CheckpointStore
}

type LocalBridge struct {
	repositories map[string]*persistence.Repository
	opts         LocalOptions
	mu           sync.Mutex
	runs         map[string]*controlplane.Run
}

func NewLocalBridge(opts LocalOptions) *LocalBridge {
	if opts.Topology == "" {
		opts.Topology = "local"
	}
	if opts.RunsDir == "" {
		opts.RunsDir = filepath.Join(".sysbox", "runs")
	}
	if opts.Log == nil {
		opts.Log = os.Stdout
	}
	if opts.CheckpointStore == nil {
		opts.CheckpointStore = localCheckpointStore{runsDir: opts.RunsDir}
	}
	return &LocalBridge{opts: opts, runs: map[string]*controlplane.Run{}}
}

func (b *LocalBridge) AttachRun(run *controlplane.Run) {
	if run == nil {
		return
	}
	b.mu.Lock()
	b.runs[run.ID] = run
	b.mu.Unlock()
}

func (b *LocalBridge) PrepareExecution(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	repo, err := b.repository(run.Topology)
	if err != nil {
		return ctx, nil, err
	}
	sources, err := repo.LoadGCSources(ctx, run.Topology)
	if err != nil {
		return ctx, nil, err
	}
	if len(sources) > 0 {
		return ctx, nil, fmt.Errorf("enrolled GC topology requires configured host executor; local CLI publication is unsupported")
	}

	if b.opts.PrepareExecution == nil {
		return ctx, func() {}, nil
	}
	return b.opts.PrepareExecution(ctx, run)
}

func (b *LocalBridge) LogWriter(string) io.Writer {
	return b.opts.Log
}

func (b *LocalBridge) LockTopology(ctx context.Context, topology string) (func(), error) {
	repo, err := b.repository(topology)
	if err != nil {
		return nil, err
	}
	return repo.LockExecution(ctx, topology)
}

func (b *LocalBridge) Finish(run *controlplane.Run, err error) {
	if run == nil {
		return
	}
	now := time.Now().UTC()
	run.EndedAt = now
	if err != nil {
		run.Status = controlplane.RunFailed
		run.Err = err.Error()
		run.Recoverable = true
	} else {
		run.Status = controlplane.RunDone
		run.Err = ""
		run.Recoverable = false
	}
	if run.Op == "reset" {
		if proofErr := b.recordResetReturn(run); proofErr != nil {
			run.Status, run.Recoverable = controlplane.RunFailed, true
			run.Err = fmt.Sprintf("%s; record reset return: %v", run.Err, proofErr)
		}
	}
	b.mu.Lock()
	b.runs[run.ID] = run
	b.mu.Unlock()
	if b.opts.AfterRun != nil {
		b.opts.AfterRun(run)
	}
}

func (b *LocalBridge) StateManager(topology string) (*state.Manager, error) {
	repo, err := b.repository(topology)
	if err != nil {
		return nil, err
	}
	return state.NewManagerWithBackend(repo.StateBackend(topology)), nil
}
func (b *LocalBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	return b.repository(topology)
}
func (b *LocalBridge) repository(topology string) (*persistence.Repository, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.repositories[topology]; r != nil {
		return r, nil
	}
	raw, path, err := persistence.ResolveLocation(b.opts.BackendURL, b.opts.StatePath, topology)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := persistence.OpenConfigured(ctx, raw, path)
	if err != nil {
		return nil, err
	}
	if b.repositories == nil {
		b.repositories = map[string]*persistence.Repository{}
	}
	b.repositories[topology] = r
	return r, nil
}

func (b *LocalBridge) HCLFile(string) string {
	return b.opts.ConfigFile
}

func (b *LocalBridge) HCLFileForRun(run *controlplane.Run) string {
	if run != nil && run.SnapshotPath != "" {
		return run.SnapshotPath
	}
	return b.opts.ConfigFile
}

func (b *LocalBridge) Topologies(context.Context) []string {
	if b.opts.Topology != "" {
		return []string{b.opts.Topology}
	}
	return topologiesFromRunsDir(b.opts.RunsDir)
}

func (b *LocalBridge) CheckpointFile(topology, runID string) string {
	if topology == "" {
		topology = b.opts.Topology
	}
	return filepath.Join(b.opts.RunsDir, topology, "runs", runID+".checkpoint.json")
}

func (b *LocalBridge) CheckpointStore() runtime.CheckpointStore {
	return b.opts.CheckpointStore
}

func (b *LocalBridge) ParentRun(ctx context.Context, id string) (*controlplane.Run, error) {
	if id == "" {
		return nil, fmt.Errorf("parent reset Run ID is required")
	}
	parent, err := b.loadResetReturn(id)
	if err != nil {
		return nil, err
	}
	if parent.ParentID != "" {
		return parent, nil
	} // ancestry resolver verifies every earlier return
	repo, err := b.repository(b.opts.Topology)
	if err != nil {
		return nil, err
	}
	actions, err := repo.LoadActions(ctx, b.opts.Topology)
	if err != nil {
		return nil, err
	}
	var queued time.Time
	var revision, topology string
	found := false
	for _, action := range actions {
		if action.RunID != id {
			continue
		}
		if !found {
			queued, revision, topology, found = action.RunQueuedAt, action.RunRevision, action.Topology, true
		} else if !queued.Equal(action.RunQueuedAt) || revision != action.RunRevision {
			return nil, fmt.Errorf("parent reset Run incarnation is inconsistent")
		}
	}
	if !found || queued.IsZero() {
		return nil, fmt.Errorf("parent reset Run %q lacks durable incarnation", id)
	}
	if parent.Topology != topology || !parent.QueuedAt.Equal(queued) || parent.Revision != revision {
		return nil, fmt.Errorf("parent reset return evidence has different incarnation")
	}
	return parent, nil
}

func (b *LocalBridge) Preflight(context.Context, *controlplane.Run, io.Writer) error {
	return nil
}

func (b *LocalBridge) BeforeApply(plan *runtime.Plan) error {
	if b.opts.BeforeApply == nil {
		return nil
	}
	return b.opts.BeforeApply(plan)
}

func (b *LocalBridge) BeforeDestroy(plan *runtime.Plan) error {
	if b.opts.BeforeDestroy == nil {
		return nil
	}
	return b.opts.BeforeDestroy(plan)
}

func (b *LocalBridge) OperationTarget() string { return b.opts.Target }

func (b *LocalBridge) BeforeReset(plan *runtime.Plan) error {
	if b.opts.BeforeReset == nil {
		return nil
	}
	return b.opts.BeforeReset(plan)
}

type localCheckpointStore struct{ runsDir string }

func (localCheckpointStore) SaveCheckpoint(context.Context, string, string, runtime.OperationCheckpoint) error {
	return nil
}

func (s localCheckpointStore) LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error) {
	if !localRunID.MatchString(topology) || !localRunID.MatchString(runID) {
		return nil, fmt.Errorf("invalid local checkpoint identity")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return runtime.LoadCheckpointFile(filepath.Join(s.runsDir, topology, "runs", runID+".checkpoint.json"))
}

func splitAddr(addr string) (string, string, error) {
	parts := strings.SplitN(addr, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected type.name, got %q", addr)
	}
	return parts[0], parts[1], nil
}
