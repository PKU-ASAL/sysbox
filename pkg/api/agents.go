package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const agentOfflineAfter = 2 * time.Minute

func (s *Server) saveAgent(ctx context.Context, agent controlplane.Agent) error {
	if agent.Protocol == "" {
		agent.Protocol = controlplane.AgentProtocolVersion
	}
	if s.apiStore != nil {
		if err := s.apiStore.SaveAgent(ctx, agent); err != nil {
			return err
		}
	}
	if s.agents != nil {
		s.agents.Save(agent)
	}
	return nil
}

func (s *Server) getAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	if id == DefaultAgentID {
		agent := localAgent()
		return &agent, nil
	}
	if s.apiStore != nil {
		if agent, err := s.apiStore.GetAgent(ctx, id); err == nil {
			if s.agents != nil {
				s.agents.Save(*agent)
			}
			return agent, nil
		}
	}
	if s.agents != nil {
		return s.agents.Get(id)
	}
	return nil, fmt.Errorf("agent not found")
}

func (s *Server) listAgents(ctx context.Context) []controlplane.Agent {
	var agents []controlplane.Agent
	if s.apiStore != nil {
		if stored, err := s.apiStore.ListAgents(ctx); err == nil {
			agents = append(agents, stored...)
		}
	}
	if len(agents) == 0 && s.agents != nil {
		agents = append(agents, s.agents.List()...)
	}
	agents = ensureLocalAgent(agents)
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	return agents
}

func (s *Server) markStaleAgentsOffline(ctx context.Context, now time.Time) {
	if s.apiStore == nil {
		return
	}
	for _, agent := range s.listAgents(ctx) {
		if agent.ID == DefaultAgentID || agent.Disabled || agent.Quarantined || agent.LastHeartbeat.IsZero() {
			continue
		}
		if agent.Status == "offline" || !agent.LastHeartbeat.Before(now.Add(-agentOfflineAfter)) {
			continue
		}
		agent.Status = "offline"
		agent.Reason = "heartbeat stale"
		agent.UpdatedAt = now
		_ = s.saveAgent(ctx, agent)
	}
}
