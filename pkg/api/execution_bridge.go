package api

import (
	"context"
	"io"
	"path/filepath"
	"time"

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

// Finish prepares the agent's completion receipt. The bridge shares the API
// database for state/checkpoints, but it must never publish a canonical run
// outcome there: only the authenticated completion endpoint owns that CAS.
func (b *ExecutionBridge) Finish(run *controlplane.Run, err error) {
	if run == nil {
		return
	}
	jobs := b.server.jobs
	jobs.mu.Lock()
	run.MarkFinished(err, time.Now().UTC())
	run.Inputs = nil
	jobs.runs[run.ID] = run
	delete(jobs.applyInputs, run.ID)
	jobs.mu.Unlock()
	jobs.logs.Close(run.ID)
}

func (b *ExecutionBridge) StateManager(topology string) (*state.Manager, error) {
	return b.server.stateManager(topology)
}

func (b *ExecutionBridge) HCLFile(topology string) string {
	return b.server.workspaceService().HCLFile(topology)
}

func (b *ExecutionBridge) HCLFileForRun(run *controlplane.Run) string {
	if run != nil && run.SnapshotPath != "" {
		return run.SnapshotPath
	}
	if run == nil {
		return ""
	}
	return b.HCLFile(run.Topology)
}

func (b *ExecutionBridge) Topologies(ctx context.Context) []string {
	mgr, err := b.server.stateManager("__list__")
	if err != nil {
		return nil
	}
	items, err := mgr.ListTopologies(ctx)
	if err != nil {
		return nil
	}
	// The local file backend has no TopologyLister. Discover only state files
	// under this agent's configured runs directory, as LocalBridge does.
	if b.server.stateBackend == "" {
		matches, err := filepath.Glob(filepath.Join(b.server.runsDir, "*", "state.json"))
		if err == nil {
			out := make([]string, 0, len(matches))
			for _, match := range matches {
				out = append(out, filepath.Base(filepath.Dir(match)))
			}
			return out
		}
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

func (b *ExecutionBridge) Preflight(ctx context.Context, topology string, log io.Writer) error {
	res, err := b.server.preflightTopology(topology)
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
