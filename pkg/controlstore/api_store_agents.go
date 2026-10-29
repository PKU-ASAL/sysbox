package controlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	raw, err := persistence.EncodeCommandEvent(event)
	if err != nil {
		return err
	}
	decoded, err := persistence.DecodeCommandEvent(raw)
	if err != nil {
		return err
	}
	event = *decoded
	agentID := event.AgentID
	if agentID == "" {
		agentID = "_unknown"
	}
	path := filepath.Join(s.RunsDir, "_agents", agentID, "command-events.jsonl")
	return appendLocalHistory(ctx, path, json.RawMessage(raw))
}

func (s *Local) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	pattern := filepath.Join(s.RunsDir, "_agents", "*", "command-events.jsonl")
	if agentID != "" {
		pattern = filepath.Join(s.RunsDir, "_agents", agentID, "command-events.jsonl")
	}
	files, err := LocalRecordPaths(pattern)
	if err != nil {
		return nil, fmt.Errorf("list local command history: %w", err)
	}
	var out []controlplane.AgentCommandEvent
	for _, f := range files {
		raws, err := ReadLocalHistory[json.RawMessage](ctx, f)
		if err != nil {
			return nil, err
		}
		for _, raw := range raws {
			event, err := persistence.DecodeCommandEvent(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, *event)
		}
	}
	return out, nil
}

func (s *Local) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).SaveAgentCommand(ctx, cmd)
}

func (s *Local) DeleteAgentCommand(ctx context.Context, agentID, commandID string) error {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).DeleteAgentCommand(ctx, agentID, commandID)
}

func (s *Local) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).ListAgentCommands(ctx, agentID)
}

func (s *Local) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).AcquireAgentCommandLease(ctx, agentID, commandID, owner, ttl)
}

func (s *Postgres) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	event = persistence.NormalizeCommandEvent(event)
	raw, err := persistence.EncodeCommandEvent(event)
	if err != nil {
		return err
	}
	conn, err := s.Connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_agent_command_events (agent_id, command_id, status, data, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5)`,
		event.AgentID, event.CommandID, event.Status, string(raw), event.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres save agent command event: %w", err)
	}
	return nil
}

func (s *Postgres) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	conn, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	query := `SELECT data::text FROM sysbox_agent_command_events ORDER BY created_at DESC LIMIT 512`
	args := []any{}
	if agentID != "" {
		query = `SELECT data::text FROM sysbox_agent_command_events WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 512`
		args = append(args, agentID)
	}
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres list agent command events: %w", err)
	}
	defer rows.Close()
	var out []controlplane.AgentCommandEvent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		event, err := persistence.DecodeCommandEvent(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, *event)
	}
	return out, rows.Err()
}

func (s *Postgres) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).SaveAgentCommand(ctx, cmd)
}

func (s *Postgres) DeleteAgentCommand(ctx context.Context, agentID, commandID string) error {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).DeleteAgentCommand(ctx, agentID, commandID)
}

func (s *Postgres) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).ListAgentCommands(ctx, agentID)
}

func (s *Postgres) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).AcquireAgentCommandLease(ctx, agentID, commandID, owner, ttl)
}
