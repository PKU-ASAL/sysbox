package api

import (
	"context"
	"io"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
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

func (b *ExecutionBridge) HCLFile(topology string) string {
	return b.server.workspaceService().HCLFile(topology)
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

func (b *ExecutionBridge) ValidateStoredPlanForApply(ctx context.Context, topology, planID string, currentSerial int64) (*controlplane.Plan, error) {
	return b.server.runs().ValidateStoredPlanForApply(ctx, topology, planID, currentSerial)
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
