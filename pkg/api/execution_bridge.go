package api

import (
	"context"
	"fmt"
	"io"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// ExecutionBridge is the API-side implementation of agentexec.Bridge: it
// gives the run executor access to control-plane services (run tracking,
// state, checkpoints, console) without agentexec importing this package.
type ExecutionBridge struct {
	server *Server
}

func NewExecutionBridge(cfg config.ServiceConfig) *ExecutionBridge {
	s := NewServerWithConfig(cfg)
	s.jobs = newJobsWithPolicy(s.runsDir, s.apiStore, false, cfg.RunClaimTTL(), cfg.RunExpiredPolicy())
	return &ExecutionBridge{server: s}
}

func (b *ExecutionBridge) AttachRun(run *controlplane.Run) {
	if run == nil {
		return
	}
	b.server.jobs.mu.Lock()
	b.server.jobs.runs[run.ID] = run
	b.server.jobs.mu.Unlock()
	b.server.jobs.logs.Ensure(run.ID, false)
}

func (b *ExecutionBridge) LogWriter(runID string) io.Writer {
	return b.server.jobs.logWriter(runID)
}

func (b *ExecutionBridge) LockTopology(topology string) func() {
	return b.server.jobs.lockTopology(topology)
}

func (b *ExecutionBridge) Finish(run *controlplane.Run, err error) {
	b.server.jobs.finish(run, err)
}

func (b *ExecutionBridge) StateManager(topology string) (*state.Manager, error) {
	return b.server.stateManager(topology)
}

func (b *ExecutionBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	return b.server.actionRepository(topology)
}

func (b *ExecutionBridge) HCLFile(topology string) string {
	return b.server.workspaceService().HCLFile(topology)
}

func (b *ExecutionBridge) HCLFileForRun(run *controlplane.Run) string {
	if run != nil && run.SnapshotPath != "" {
		return run.SnapshotPath
	}
	return ""
}

// MaterializeRunRevision resolves an immutable global revision through the
// canonical repository and pins it in the host workspace tree. Host
// supervisors use this entry point instead of accepting a worker supplied
// filesystem path.
func (b *ExecutionBridge) MaterializeRunRevision(ctx context.Context, topology, revision string) (string, error) {
	if b == nil || b.server == nil || topology == "" || revision == "" {
		return "", fmt.Errorf("immutable run revision is required")
	}
	value, err := b.server.apiStore.GetGlobalRevision(ctx, revision)
	if err != nil {
		return "", err
	}
	return b.server.workspaceService().MaterializeRevision(topology, revision, value.Files)
}

func (b *ExecutionBridge) Topologies(ctx context.Context) []string {
	items, err := b.server.workspaceService().List(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.HasState {
			out = append(out, item.Name)
		}
	}
	return out
}

func (b *ExecutionBridge) CheckpointFile(topology, runID string) string {
	return b.server.checkpointFile(topology, runID)
}

func (b *ExecutionBridge) CheckpointStore() runtime.CheckpointStore {
	return b.server.apiStore
}

func (b *ExecutionBridge) ParentRun(ctx context.Context, id string) (*controlplane.Run, error) {
	return b.server.apiStore.GetRun(ctx, id)
}

func (b *ExecutionBridge) ReconcileParentJournal(parent, run *controlplane.Run) error {
	return b.server.reconcileParentJournal(parent, run)
}

func (b *ExecutionBridge) Preflight(ctx context.Context, run *controlplane.Run, log io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := agentexec.RunWorkspace(ctx, run)
	if path == "" {
		return fmt.Errorf("run preflight requires an immutable snapshot")
	}
	res, err := preflightFile(path)
	if err != nil {
		return err
	}
	writePreflightLogsTo(log, res)
	return res.err()
}

func (b *ExecutionBridge) BuildResetPlan(g *graph.Graph, st *state.State, target string) (*runtime.Plan, error) {
	return runtime.BuildResetPlan(g, st, target)
}

func (b *ExecutionBridge) BeforeReset(*runtime.Plan) error { return nil }

func (b *ExecutionBridge) ResumeResetPlan(ctx context.Context, parent *controlplane.Run, current *runtime.Plan) (*runtime.Plan, error) {
	cp, err := b.server.apiStore.LoadCheckpoint(ctx, parent.Topology, parent.ID)
	if err != nil {
		return nil, err
	}
	return runtime.ResumeResetPlan(cp, current)
}

func (b *ExecutionBridge) OpenConsole(ctx context.Context, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest, ws *websocket.Conn) error {
	mgr, err := b.StateManager(sess.Topology)
	if err != nil {
		return err
	}
	st, err := mgr.Load()
	if err != nil {
		return err
	}
	return agentexec.OpenConsoleFromState(ctx, st, sess, req, ws)
}
