package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type SchedulerService struct {
	jobs      *Jobs
	agents    *AgentService
	publish   func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)
	placement *WorkspaceService
}

func newSchedulerService(server *Server) *SchedulerService {
	agentSvc := server.agentService()
	return &SchedulerService{
		jobs:   server.jobs,
		agents: agentSvc,
		publish: func(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
			return agentSvc.PublishCommand(ctx, agentID, cmd)
		},
		placement: server.workspaceService(),
	}
}

func (s *SchedulerService) DispatchRun(ctx context.Context, run *controlplane.Run, required []string) error {
	agent, err := s.SelectAgentForTopology(ctx, run.Topology, required, run.AgentID)
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

// SelectAgentForTopology enforces the durable host affinity for host-local
// resources before selecting an agent.
func (s *SchedulerService) SelectAgentForTopology(ctx context.Context, topology string, required []string, preferred string) (controlplane.Agent, error) {
	if p, err := s.placement.loadPlacement(topology); err != nil {
		return controlplane.Agent{}, err
	} else if p != nil {
		if preferred != "" && preferred != p.AgentID {
			return controlplane.Agent{}, fmt.Errorf("topology %q is placed on agent %q", topology, p.AgentID)
		}
		preferred = p.AgentID
	}
	agent, err := s.SelectAgent(ctx, required, preferred)
	if err != nil {
		return controlplane.Agent{}, err
	}
	if err := s.placement.bindPlacement(topology, agent.ID, agent.Protocol); err != nil {
		return controlplane.Agent{}, err
	}
	return agent, nil
}

func (s *SchedulerService) SelectAgent(ctx context.Context, required []string, preferred string) (controlplane.Agent, error) {
	agents := s.agents.List(ctx)
	required = normalizeCapabilities(required)
	if preferred != "" {
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
