package hostexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Opts         LocalOptions
	Mu           sync.Mutex
	Runs         map[string]*controlplane.Run
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
	return &LocalBridge{Opts: opts, Runs: map[string]*controlplane.Run{}}
}

func (b *LocalBridge) AttachRun(run *controlplane.Run) {
	if run == nil {
		return
	}
	b.Mu.Lock()
	b.Runs[run.ID] = run
	b.Mu.Unlock()
}

func (b *LocalBridge) PrepareExecution(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	repo, err := b.Repository(run.Topology)
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

	if b.Opts.PrepareExecution == nil {
		return ctx, func() {}, nil
	}
	return b.Opts.PrepareExecution(ctx, run)
}

func (b *LocalBridge) LogWriter(string) io.Writer {
	return b.Opts.Log
}

func (b *LocalBridge) LockTopology(ctx context.Context, topology string) (func(), error) {
	repo, err := b.Repository(topology)
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
		if proofErr := b.RecordResetReturn(run); proofErr != nil {
			run.Status, run.Recoverable = controlplane.RunFailed, true
			run.Err = fmt.Sprintf("%s; record reset return: %v", run.Err, proofErr)
		}
	}
	b.Mu.Lock()
	b.Runs[run.ID] = run
	b.Mu.Unlock()
	if b.Opts.AfterRun != nil {
		b.Opts.AfterRun(run)
	}
}

func (b *LocalBridge) StateManager(topology string) (*state.Manager, error) {
	repo, err := b.Repository(topology)
	if err != nil {
		return nil, err
	}
	return state.NewManagerWithBackend(repo.StateBackend(topology)), nil
}

func (b *LocalBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	return b.Repository(topology)
}

func (b *LocalBridge) Repository(topology string) (*persistence.Repository, error) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if r := b.repositories[topology]; r != nil {
		return r, nil
	}
	raw, path, err := persistence.ResolveLocation(b.Opts.BackendURL, b.Opts.StatePath, topology)
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
	return b.Opts.ConfigFile
}

func (b *LocalBridge) HCLFileForRun(run *controlplane.Run) string {
	if run != nil && run.SnapshotPath != "" {
		return run.SnapshotPath
	}
	return b.Opts.ConfigFile
}

func (b *LocalBridge) Topologies(context.Context) []string {
	if b.Opts.Topology != "" {
		return []string{b.Opts.Topology}
	}
	return topologiesFromRunsDir(b.Opts.RunsDir)
}

func (b *LocalBridge) CheckpointFile(topology, runID string) string {
	if topology == "" {
		topology = b.Opts.Topology
	}
	return filepath.Join(b.Opts.RunsDir, topology, "runs", runID+".checkpoint.json")
}

func (b *LocalBridge) CheckpointStore() runtime.CheckpointStore {
	return b.Opts.CheckpointStore
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
	repo, err := b.Repository(b.Opts.Topology)
	if err != nil {
		return nil, err
	}
	actions, err := repo.LoadActions(ctx, b.Opts.Topology)
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
	if b.Opts.BeforeApply == nil {
		return nil
	}
	return b.Opts.BeforeApply(plan)
}

func (b *LocalBridge) BeforeDestroy(plan *runtime.Plan) error {
	if b.Opts.BeforeDestroy == nil {
		return nil
	}
	return b.Opts.BeforeDestroy(plan)
}

func (b *LocalBridge) OperationTarget() string { return b.Opts.Target }

func (b *LocalBridge) BeforeReset(plan *runtime.Plan) error {
	if b.Opts.BeforeReset == nil {
		return nil
	}
	return b.Opts.BeforeReset(plan)
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
