package api

import (
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// HostExecutionBridge records executor outcomes locally. The canonical run is
// completed separately by the API after verifying the host's signed receipt.
type HostExecutionBridge struct {
	*ExecutionBridge
}

func NewHostExecutionBridge(cfg config.ServiceConfig) (*HostExecutionBridge, error) {
	bridge := NewExecutionBridge(cfg)
	if bridge.server.initErr != nil {
		return nil, bridge.server.initErr
	}
	return &HostExecutionBridge{ExecutionBridge: bridge}, nil
}

func (b *HostExecutionBridge) Finish(run *controlplane.Run, err error) {
	if run == nil {
		return
	}
	jobs := b.server.jobs
	jobs.mu.Lock()
	run.MarkFinished(err, time.Now())
	run.Inputs = nil
	jobs.runs[run.ID] = run
	jobs.mu.Unlock()
	jobs.logs.Close(run.ID)
}
