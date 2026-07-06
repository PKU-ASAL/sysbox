package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type SchedulerService struct {
	jobs    *Jobs
	agents  *AgentService
	publish func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)
}

func newSchedulerService(server *Server) *SchedulerService {
	agentSvc := server.agentService()
	return &SchedulerService{
		jobs:   server.jobs,
		agents: agentSvc,
		publish: func(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
			return agentSvc.PublishCommand(ctx, agentID, cmd)
		},
	}
}

func (s *SchedulerService) DispatchRun(ctx context.Context, run *controlplane.Run, required []string) error {
	agent, err := s.SelectAgent(ctx, required, run.AgentID)
	if err != nil {
		s.jobs.finish(run, err)
		return err
	}
	s.jobs.assign(run, agent.ID)
	if _, err := s.publish(ctx, agent.ID, controlplaneRunAssignedCommand(run)); err != nil {
		return err
	}
	return nil
}

func (s *SchedulerService) SelectAgent(ctx context.Context, required []string, preferred string) (controlplane.Agent, error) {
	agents := s.agents.List(ctx)
	required = normalizeCapabilities(required)
	if preferred != "" && preferred != DefaultAgentID {
		for _, agent := range agents {
			if agent.ID == preferred {
				if !agent.IsSchedulable() {
					return controlplane.Agent{}, fmt.Errorf("agent %q is not online", preferred)
				}
				if !hasCapabilities(agent.Capabilities, required) {
					return controlplane.Agent{}, fmt.Errorf("agent %q does not satisfy capabilities: required %v, has %v", preferred, required, normalizeCapabilities(agent.Capabilities))
				}
				return agent, nil
			}
		}
		return controlplane.Agent{}, fmt.Errorf("agent %q not found", preferred)
	}
	for _, agent := range agents {
		if !agent.IsSchedulable() {
			continue
		}
		if hasCapabilities(agent.Capabilities, required) {
			return agent, nil
		}
	}
	return controlplane.Agent{}, fmt.Errorf("no online agent satisfies capabilities: %v", required)
}
