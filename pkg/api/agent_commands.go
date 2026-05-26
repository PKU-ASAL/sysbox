package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Server) publishAgentCommand(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}
	if cmd.AgentID == "" {
		cmd.AgentID = agentID
	}
	if cmd.Status == "" {
		cmd.Status = "queued"
	}
	if cmd.CreatedAt.IsZero() {
		cmd.CreatedAt = time.Now().UTC()
	}
	if s.apiStore != nil {
		if err := s.apiStore.SaveAgentCommand(ctx, cmd); err != nil {
			return cmd, err
		}
	}
	if s.agents != nil {
		return cmd, s.agents.PublishCommand(agentID, cmd)
	}
	return cmd, nil
}

func commandIsPending(cmd controlplane.AgentCommand) bool {
	switch cmd.Status {
	case "", "queued", "delivered":
		return true
	default:
		return false
	}
}

func commandTerminal(status string) bool {
	switch status {
	case "completed", "failed", "denied", "cancelled":
		return true
	default:
		return false
	}
}

func (s *Server) acquireAgentCommandLease(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, bool) {
	now := time.Now().UTC()
	if cmd.AgentID != agentID {
		return cmd, false
	}
	if !commandIsPending(cmd) {
		return cmd, false
	}
	if !cmd.LeaseUntil.IsZero() && cmd.LeaseUntil.After(now) && cmd.LeaseOwner != "" {
		return cmd, false
	}
	cmd.Status = "leased"
	cmd.LeaseOwner = fmt.Sprintf("%s:%d", agentID, now.UnixNano())
	cmd.LeaseUntil = now.Add(30 * time.Second)
	cmd.Attempt++
	if s.apiStore != nil {
		if err := s.apiStore.SaveAgentCommand(ctx, cmd); err != nil {
			return cmd, false
		}
	}
	return cmd, true
}

func deliverAgentCommand(cmd controlplane.AgentCommand) controlplane.AgentCommand {
	cmd.Status = "delivered"
	cmd.Delivered = time.Now().UTC()
	return cmd
}
