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

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
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
		opts.CheckpointStore = localCheckpointStore{}
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

func (b *LocalBridge) LogWriter(string) io.Writer {
	return b.opts.Log
}

func (b *LocalBridge) LockTopology(string) func() {
	return func() {}
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

func (b *LocalBridge) ParentRun(context.Context, string) (*controlplane.Run, error) {
	return nil, fmt.Errorf("parent runs require the API control plane")
}

func (b *LocalBridge) ReconcileParentJournal(_, _ *controlplane.Run) error {
	return fmt.Errorf("run resume requires the API control plane")
}

func (b *LocalBridge) Preflight(context.Context, string, io.Writer) error {
	return nil
}

func (b *LocalBridge) OpenConsole(ctx context.Context, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest, ws *websocket.Conn) error {
	mgr, err := b.StateManager(sess.Topology)
	if err != nil {
		return err
	}
	st, err := mgr.Load()
	if err != nil {
		return err
	}
	return OpenConsoleFromState(ctx, st, sess, req, ws)
}

func (b *LocalBridge) FilterApplyPlan(plan *runtime.Plan) (*runtime.Plan, error) {
	if b.opts.Target == "" {
		return plan, nil
	}
	typ, name, err := splitAddr(b.opts.Target)
	if err != nil {
		return nil, fmt.Errorf("--target: %w", err)
	}
	fmt.Fprintf(b.opts.Log, "Targeting: %s.%s\n", typ, name)
	return runtime.FilterPlanByTarget(plan, typ, name), nil
}

func (b *LocalBridge) RefreshApply() bool {
	return b.opts.Refresh
}

func (b *LocalBridge) BeforeApply(plan *runtime.Plan) error {
	if b.opts.BeforeApply == nil {
		return nil
	}
	return b.opts.BeforeApply(plan)
}

func (b *LocalBridge) BuildDestroyPlan(st *state.State) (*runtime.Plan, error) {
	plan := &runtime.Plan{}
	for i := len(st.Resources) - 1; i >= 0; i-- {
		r := st.Resources[i]
		if r.LifecyclePreventDestroy() {
			return nil, fmt.Errorf("%s: lifecycle.prevent_destroy blocks deletion", r.Address)
		}
		plan.Actions = append(plan.Actions, controlplane.PlannedChange{
			Address: r.Address, Action: controlplane.PlanActionDelete, Reason: "destroy requested",
		})
	}
	return plan, nil
}

func (b *LocalBridge) BeforeDestroy(plan *runtime.Plan) error {
	if b.opts.BeforeDestroy == nil {
		return nil
	}
	return b.opts.BeforeDestroy(plan)
}

func (b *LocalBridge) BuildResetPlan(g *graph.Graph, st *state.State, target string) (*runtime.Plan, error) {
	if target == "" {
		target = b.opts.Target
	}
	return runtime.BuildResetPlan(g, st, target)
}

func (b *LocalBridge) BeforeReset(plan *runtime.Plan) error {
	if b.opts.BeforeReset == nil {
		return nil
	}
	return b.opts.BeforeReset(plan)
}

func (b *LocalBridge) ResumeResetPlan(context.Context, *controlplane.Run, *runtime.Plan) (*runtime.Plan, error) {
	return nil, fmt.Errorf("local reset runs do not support parent-run resume")
}

type localCheckpointStore struct{}

func (localCheckpointStore) SaveCheckpoint(context.Context, string, string, runtime.OperationCheckpoint) error {
	return nil
}

func splitAddr(addr string) (string, string, error) {
	parts := strings.SplitN(addr, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected type.name, got %q", addr)
	}
	return parts[0], parts[1], nil
}
