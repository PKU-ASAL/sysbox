package api

import (
	"context"
	"fmt"
	"time"

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
	assigned := *run
	transientInputs := cloneInputs(run.Inputs)
	assigned.MarkAssigned(agent.ID, time.Now().UTC())
	command := controlplaneRunAssignedCommand(&assigned)
	command.ID = "run-" + run.ID
	command.AgentID = agent.ID
	command.Status = controlplane.AgentCommandStatusQueued
	command.Protocol = controlplane.AgentProtocolVersion
	command.CreatedAt = time.Now().UTC()
	fingerprint := fmt.Sprintf("run-dispatch:%s:%s:%s", assigned.ID, assigned.Op, assigned.AgentID)
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, RunDispatchRequest{Run: assigned, Command: command, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	*run = *stored
	run.Inputs = transientInputs
	s.jobs.remember(stored)
	s.jobs.mu.Lock()
	if cached := s.jobs.runs[stored.ID]; cached != nil {
		cached.Inputs = transientInputs
	}
	s.jobs.mu.Unlock()
	if created && s.agents.registry != nil {
		_ = s.agents.registry.PublishCommand(agent.ID, command)
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
	agents, err := s.agents.List(ctx)
	if err != nil {
		return controlplane.Agent{}, err
	}
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
