package api

import (
	"context"
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
	case "", "queued", "delivered", "acked", "running":
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
