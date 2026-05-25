package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
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

func (b *ExecutionBridge) Execute(run *Run) {
	b.server.executeRunLocally(run)
}

func (s *Server) executeRunLocally(run *Run) {
	if run == nil {
		return
	}
	run.logs = &Broadcaster{}
	s.jobs.mu.Lock()
	s.jobs.runs[run.ID] = run
	s.jobs.mu.Unlock()
	switch run.Op {
	case "apply":
		if run.ParentID != "" {
			if parent, err := s.apiStore.GetRun(context.Background(), run.ParentID); err == nil {
				s.runResumeApply(parent, run)
				return
			}
		}
		s.runApply(run.Topology, run)
	case "destroy":
		if run.ParentID != "" {
			if parent, err := s.apiStore.GetRun(context.Background(), run.ParentID); err == nil {
				s.runResumeDestroy(parent, run)
				return
			}
		}
		s.runDestroy(run.Topology, run)
	default:
		s.jobs.finish(run, fmt.Errorf("unsupported run op %q", run.Op))
	}
}
