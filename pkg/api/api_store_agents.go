package api

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
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
	path := filepath.Join(s.runsDir, "_agents", agentID, "command-events.jsonl")
	return appendLocalHistory(ctx, path, json.RawMessage(raw))
}

func (s *localAPIStore) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	pattern := filepath.Join(s.runsDir, "_agents", "*", "command-events.jsonl")
	if agentID != "" {
		pattern = filepath.Join(s.runsDir, "_agents", agentID, "command-events.jsonl")
	}
	files, err := localRecordPaths(pattern)
	if err != nil {
		return nil, fmt.Errorf("list local command history: %w", err)
	}
	var out []controlplane.AgentCommandEvent
	for _, f := range files {
		raws, err := readLocalHistory[json.RawMessage](ctx, f)
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

func (s *localAPIStore) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	return persistence.NewCommandRepository(localCommandStorage{store: s}).SaveAgentCommand(ctx, cmd)
}
func (s *localAPIStore) DeleteAgentCommand(ctx context.Context, agentID, commandID string) error {
	return persistence.NewCommandRepository(localCommandStorage{store: s}).DeleteAgentCommand(ctx, agentID, commandID)
}
func (s *localAPIStore) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(localCommandStorage{store: s}).ListAgentCommands(ctx, agentID)
}
func (s *localAPIStore) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	return persistence.NewCommandRepository(localCommandStorage{store: s}).AcquireAgentCommandLease(ctx, agentID, commandID, owner, ttl)
}

func (s *postgresAPIStore) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	event = persistence.NormalizeCommandEvent(event)
	raw, err := persistence.EncodeCommandEvent(event)
	if err != nil {
		return err
	}
	conn, err := s.connect(ctx)
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

func (s *postgresAPIStore) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	conn, err := s.connect(ctx)
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

func (s *postgresAPIStore) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	return persistence.NewCommandRepository(postgresCommandStorage{store: s}).SaveAgentCommand(ctx, cmd)
}
func (s *postgresAPIStore) DeleteAgentCommand(ctx context.Context, agentID, commandID string) error {
	return persistence.NewCommandRepository(postgresCommandStorage{store: s}).DeleteAgentCommand(ctx, agentID, commandID)
}
func (s *postgresAPIStore) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(postgresCommandStorage{store: s}).ListAgentCommands(ctx, agentID)
}
func (s *postgresAPIStore) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	return persistence.NewCommandRepository(postgresCommandStorage{store: s}).AcquireAgentCommandLease(ctx, agentID, commandID, owner, ttl)
}
