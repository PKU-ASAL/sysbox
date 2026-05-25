package api

import (
	"context"
	"io"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// ExecutionBridge is the temporary compatibility surface used by pkg/worker
// while apply/destroy execution is being moved out of the API package.
type ExecutionBridge struct {
	server *Server
}

func NewExecutionBridge(cfg config.ServiceConfig) *ExecutionBridge {
	s := NewServerWithConfig(cfg)
	s.jobs = newJobsWithRecovery(s.runsDir, s.apiStore, false)
	return &ExecutionBridge{server: s}
}

func (b *ExecutionBridge) AttachRun(run *controlplane.Run) {
	if run == nil {
		return
	}
	b.server.jobs.mu.Lock()
	b.server.jobs.runs[run.ID] = run
	b.server.jobs.ensureLogsLocked(run.ID, false)
	b.server.jobs.mu.Unlock()
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
	return b.server.hclFile(topology)
}

func (b *ExecutionBridge) CheckpointFile(topology, runID string) string {
	return b.server.checkpointFile(topology, runID)
}

func (b *ExecutionBridge) CheckpointStore() runtime.CheckpointStore {
	return b.server.apiStore
}

func (b *ExecutionBridge) ValidateStoredPlanForApply(ctx context.Context, topology, planID string, currentSerial int64) (*controlplane.Plan, error) {
	return b.server.validateStoredPlanForApply(ctx, topology, planID, currentSerial)
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
