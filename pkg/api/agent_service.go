package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type AgentService struct {
	store           apiStore
	registry        *agentRegistry
	jobs            *Jobs
	consoles        *consoleSessionHub
	commandTTL      func() time.Duration
	agentOfflineTTL func() time.Duration
}

func newAgentService(server *Server) *AgentService {
	return &AgentService{
		store:           server.apiStore,
		registry:        server.agents,
		jobs:            server.jobs,
		consoles:        server.consoles,
		commandTTL:      server.cfg.AgentCommandTTL,
		agentOfflineTTL: server.cfg.AgentOfflineAfter,
	}
}

func (s *AgentService) Save(ctx context.Context, agent controlplane.Agent) error {
	if agent.Protocol == "" {
		agent.Protocol = controlplane.AgentProtocolVersion
	}
	if s.store != nil {
		if err := s.store.SaveAgent(ctx, agent); err != nil {
			return err
		}
	}
	if s.registry != nil {
		s.registry.Save(agent)
	}
	return nil
}

func (s *AgentService) Get(ctx context.Context, id string) (*controlplane.Agent, error) {
	if s.store != nil {
		if agent, err := s.store.GetAgent(ctx, id); err == nil {
			if s.registry != nil {
				s.registry.Save(*agent)
			}
			return agent, nil
		}
	}
	if s.registry != nil {
		return s.registry.Get(id)
	}
	return nil, fmt.Errorf("agent not found")
}

func (s *AgentService) List(ctx context.Context) []controlplane.Agent {
	agents := []controlplane.Agent{}
	if s.store != nil {
		if stored, err := s.store.ListAgents(ctx); err == nil {
			agents = append(agents, stored...)
		}
	}
	if len(agents) == 0 && s.registry != nil {
		agents = append(agents, s.registry.List()...)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	return agents
}

func (s *AgentService) MarkStaleOffline(ctx context.Context, now time.Time) {
	if s.store == nil {
		return
	}
	offlineAfter := s.agentOfflineAfter()
	for _, agent := range s.List(ctx) {
		if agent.Disabled || agent.Quarantined || agent.LastHeartbeat.IsZero() {
			continue
		}
		if agent.Status == controlplane.AgentStatusOffline || !agent.LastHeartbeat.Before(now.Add(-offlineAfter)) {
			continue
		}
		agent.Status = controlplane.AgentStatusOffline
		agent.Reason = "heartbeat stale"
		agent.UpdatedAt = now
		_ = s.Save(ctx, agent)
	}
}

func (s *AgentService) agentOfflineAfter() time.Duration {
	if s.agentOfflineTTL == nil {
		return 0
	}
	return s.agentOfflineTTL()
}

func (s *AgentService) CompleteRun(agentID, runID string, completion controlplane.RunCompletion) (controlplane.RunCompletion, error) {
	if completion.Run.ID == "" {
		completion.Run.ID = runID
	}
	if completion.Run.ID != runID {
		return completion, fmt.Errorf("completion run id mismatch")
	}
	if completion.Run.AgentID == "" {
		completion.Run.AgentID = agentID
	}
	if completion.Run.AgentID != agentID {
		return completion, fmt.Errorf("completion agent id mismatch")
	}
	s.jobs.replace(&completion.Run)
	if completion.Projection.AgentID == "" {
		completion.Projection.AgentID = agentID
	}
	if completion.Projection.Topology == "" {
		completion.Projection.Topology = completion.Run.Topology
	}
	if completion.Projection.Workspace == "" {
		completion.Projection.Workspace = completion.Run.Workspace
	}
	if completion.Projection.UpdatedAt.IsZero() {
		completion.Projection.UpdatedAt = time.Now().UTC()
	}
	s.registry.SaveProjection(completion.Projection)
	return completion, nil
}

func (s *AgentService) PublishCommand(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}
	if cmd.AgentID == "" {
		cmd.AgentID = agentID
	}
	if cmd.Status == "" {
		cmd.Status = controlplane.AgentCommandStatusQueued
	}
	if cmd.Protocol == "" {
		cmd.Protocol = controlplane.AgentProtocolVersion
	}
	if cmd.CreatedAt.IsZero() {
		cmd.CreatedAt = time.Now().UTC()
	}
	if s.store != nil {
		if err := s.store.SaveAgentCommand(ctx, cmd); err != nil {
			return cmd, err
		}
	}
	if s.registry != nil {
		return cmd, s.registry.PublishCommand(agentID, cmd)
	}
	return cmd, nil
}

func (s *AgentService) AcquireCommandLease(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, bool) {
	now := time.Now().UTC()
	if cmd.AgentID != agentID || !cmd.IsPending() || !cmd.Leasable(now) {
		return cmd, false
	}
	owner := fmt.Sprintf("%s:%d", agentID, now.UnixNano())
	ttl := s.agentCommandTTL()
	if s.store != nil {
		leased, ok, err := s.store.AcquireAgentCommandLease(ctx, agentID, cmd.ID, owner, ttl)
		if err != nil || !ok || leased == nil {
			return cmd, false
		}
		return *leased, true
	}
	cmd.MarkLeased(owner, ttl, now)
	return cmd, true
}

func (s *AgentService) agentCommandTTL() time.Duration {
	if s.commandTTL == nil {
		return time.Minute
	}
	return s.commandTTL()
}

func (s *AgentService) FindCommand(ctx context.Context, agentID, commandID string) (controlplane.AgentCommand, error) {
	if s.store == nil {
		return controlplane.AgentCommand{}, fmt.Errorf("agent command store not configured")
	}
	commands, err := s.store.ListAgentCommands(ctx, agentID)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	for _, cmd := range commands {
		if cmd.ID == commandID {
			return cmd, nil
		}
	}
	return controlplane.AgentCommand{}, fmt.Errorf("agent command not found")
}

func (s *AgentService) RecordCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	s.updateCommandFromEvent(ctx, event)
	if s.registry != nil {
		s.registry.SaveCommandEvent(event)
	}
	if s.store != nil {
		_ = s.store.SaveAgentCommandEvent(ctx, event)
	}
}

func (s *AgentService) updateCommandFromEvent(ctx context.Context, event controlplane.AgentCommandEvent) {
	if s.store == nil || event.CommandID == "" || event.AgentID == "" {
		return
	}
	cmd, err := s.FindCommand(ctx, event.AgentID, event.CommandID)
	if err != nil {
		return
	}
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	switch event.Status {
	case "ack":
		cmd.Status = "acked"
		cmd.AckedAt = now
	case "started":
		cmd.Status = controlplane.AgentCommandStatusRunning
		s.updateConsoleSessionFromCommandEvent(cmd, event)
	case controlplane.AgentCommandStatusCompleted:
		cmd.Status = controlplane.AgentCommandStatusCompleted
		cmd.EndedAt = now
		s.updateConsoleSessionFromCommandEvent(cmd, event)
	case controlplane.AgentCommandStatusFailed, controlplane.AgentCommandStatusDenied, controlplane.AgentCommandStatusCancelled:
		cmd.Status = event.Status
		cmd.Err = event.Error
		cmd.EndedAt = now
		s.updateConsoleSessionFromCommandEvent(cmd, event)
	}
	_ = s.store.SaveAgentCommand(ctx, cmd)
}

func (s *AgentService) updateConsoleSessionFromCommandEvent(cmd controlplane.AgentCommand, event controlplane.AgentCommandEvent) {
	if cmd.Type != "session_open" || cmd.Session == nil || cmd.Session.ID == "" {
		return
	}
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if s.consoles == nil {
		return
	}
	s.consoles.Update(cmd.Session.ID, func(sess *controlplane.ConsoleSession) {
		switch event.Status {
		case "started":
			if sess.Status == controlplane.ConsoleSessionStatusQueued {
				sess.Status = controlplane.ConsoleSessionStatusRunning
			}
			if sess.StartedAt.IsZero() {
				sess.StartedAt = now
			}
		case "completed":
			if sess.Status != controlplane.ConsoleSessionStatusFailed && sess.Status != controlplane.ConsoleSessionStatusCancelled {
				sess.Status = controlplane.ConsoleSessionStatusClosed
			}
			if sess.EndedAt.IsZero() {
				sess.EndedAt = now
			}
		case "failed", "denied", "cancelled":
			sess.Status = event.Status
			sess.Err = event.Error
			if sess.Err == "" {
				sess.Err = event.Message
			}
			sess.EndedAt = now
		}
	})
}

func deliverAgentCommand(cmd controlplane.AgentCommand) controlplane.AgentCommand {
	cmd.MarkDelivered(time.Now().UTC())
	return cmd
}

func commandTerminal(status string) bool {
	return controlplane.AgentCommand{Status: status}.IsTerminal()
}
