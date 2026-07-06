package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *localAPIStore) SaveAgentCommandEvent(_ context.Context, event controlplane.AgentCommandEvent) error {
	agentID := event.AgentID
	if agentID == "" {
		agentID = "_unknown"
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	path := filepath.Join(s.runsDir, "_agents", agentID, "command-events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	return json.NewEncoder(fh).Encode(event)
}

func (s *localAPIStore) ListAgentCommandEvents(_ context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	pattern := filepath.Join(s.runsDir, "_agents", "*", "command-events.jsonl")
	if agentID != "" {
		pattern = filepath.Join(s.runsDir, "_agents", agentID, "command-events.jsonl")
	}
	files, _ := filepath.Glob(pattern)
	var out []controlplane.AgentCommandEvent
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			var event controlplane.AgentCommandEvent
			if err := json.Unmarshal(sc.Bytes(), &event); err == nil {
				out = append(out, event)
			}
		}
		fh.Close()
	}
	return out, nil
}

func (s *localAPIStore) SaveAgentCommand(_ context.Context, cmd controlplane.AgentCommand) error {
	agentID := cmd.AgentID
	if agentID == "" {
		agentID = "_unknown"
	}
	if cmd.Protocol == "" {
		cmd.Protocol = controlplane.AgentProtocolVersion
	}
	return writeLocalObject(filepath.Join(s.runsDir, "_agents", agentID, "commands", cmd.ID+".json"), cmd)
}

func (s *localAPIStore) ListAgentCommands(_ context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	if agentID == "" {
		return readLocalObjects[controlplane.AgentCommand](filepath.Join(s.runsDir, "_agents", "*", "commands", "*.json"))
	}
	return readLocalObjects[controlplane.AgentCommand](filepath.Join(s.runsDir, "_agents", agentID, "commands", "*.json"))
}

func (s *localAPIStore) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	commands, err := s.ListAgentCommands(ctx, agentID)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	for _, cmd := range commands {
		if cmd.ID != commandID || cmd.AgentID != agentID {
			continue
		}
		if !cmd.Leasable(now) {
			return &cmd, false, nil
		}
		cmd.MarkLeased(owner, ttl, now)
		if err := s.SaveAgentCommand(ctx, cmd); err != nil {
			return nil, false, err
		}
		return &cmd, true, nil
	}
	return nil, false, fmt.Errorf("agent command not found")
}

func (s *localAPIStore) SaveAgentInventory(_ context.Context, inv controlplane.AgentInventory) error {
	agentID := inv.AgentID
	if agentID == "" {
		agentID = "_unknown"
	}
	return writeLocalObject(filepath.Join(s.runsDir, "_agents", agentID, "inventory.json"), inv)
}

func (s *localAPIStore) GetAgentInventory(_ context.Context, agentID string) (*controlplane.AgentInventory, error) {
	raw, err := os.ReadFile(filepath.Join(s.runsDir, "_agents", agentID, "inventory.json"))
	if err != nil {
		return nil, fmt.Errorf("agent inventory not found")
	}
	var inv controlplane.AgentInventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

func (s *localAPIStore) SaveAgent(_ context.Context, agent controlplane.Agent) error {
	if agent.ID == "" {
		return fmt.Errorf("agent id is required")
	}
	if agent.Protocol == "" {
		agent.Protocol = controlplane.AgentProtocolVersion
	}
	return writeLocalObject(filepath.Join(s.runsDir, "_agents", agent.ID, "agent.json"), agent)
}

func (s *localAPIStore) GetAgent(_ context.Context, id string) (*controlplane.Agent, error) {
	raw, err := os.ReadFile(filepath.Join(s.runsDir, "_agents", id, "agent.json"))
	if err != nil {
		return nil, fmt.Errorf("agent not found")
	}
	var agent controlplane.Agent
	if err := json.Unmarshal(raw, &agent); err != nil {
		return nil, err
	}
	return &agent, nil
}

func (s *localAPIStore) ListAgents(_ context.Context) ([]controlplane.Agent, error) {
	return readLocalObjects[controlplane.Agent](filepath.Join(s.runsDir, "_agents", "*", "agent.json"))
}

func (s *postgresAPIStore) SaveAgent(ctx context.Context, agent controlplane.Agent) error {
	if agent.ID == "" {
		return fmt.Errorf("agent id is required")
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(agent)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_agents (id, status, disabled, quarantined, protocol, secret_hash, data, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, now())
ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, disabled=EXCLUDED.disabled, quarantined=EXCLUDED.quarantined, protocol=EXCLUDED.protocol, secret_hash=EXCLUDED.secret_hash, data=EXCLUDED.data, updated_at=now()`,
		agent.ID, agent.Status, agent.Disabled, agent.Quarantined, agent.Protocol, agent.SecretHash, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save agent: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_agents WHERE id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("agent not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get agent: %w", err)
	}
	var agent controlplane.Agent
	if err := json.Unmarshal(raw, &agent); err != nil {
		return nil, err
	}
	return &agent, nil
}

func (s *postgresAPIStore) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT data::text FROM sysbox_agents ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres list agents: %w", err)
	}
	defer rows.Close()
	var out []controlplane.Agent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var agent controlplane.Agent
		if err := json.Unmarshal(raw, &agent); err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	if event.AgentID == "" {
		event.AgentID = "_unknown"
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
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
	defer conn.Close(ctx)
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
		var event controlplane.AgentCommandEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	if cmd.AgentID == "" {
		cmd.AgentID = "_unknown"
	}
	if cmd.Status == "" {
		cmd.Status = controlplane.AgentCommandStatusQueued
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	var leaseUntil any
	if !cmd.LeaseUntil.IsZero() {
		leaseUntil = cmd.LeaseUntil
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_agent_commands (agent_id, id, status, lease_owner, lease_until, attempt, data, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, now())
ON CONFLICT (id) DO UPDATE SET agent_id=EXCLUDED.agent_id, status=EXCLUDED.status, lease_owner=EXCLUDED.lease_owner, lease_until=EXCLUDED.lease_until, attempt=EXCLUDED.attempt, data=EXCLUDED.data, updated_at=now()`,
		cmd.AgentID, cmd.ID, cmd.Status, cmd.LeaseOwner, leaseUntil, cmd.Attempt, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save agent command: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var raw []byte
	var status string
	var leaseUntil *time.Time
	err = tx.QueryRow(ctx, `
SELECT data::text, status, lease_until
FROM sysbox_agent_commands
WHERE agent_id=$1 AND id=$2
FOR UPDATE`, agentID, commandID).Scan(&raw, &status, &leaseUntil)
	if err == pgx.ErrNoRows {
		return nil, false, fmt.Errorf("agent command not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres load agent command lease row: %w", err)
	}
	var cmd controlplane.AgentCommand
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return nil, false, err
	}
	cmd.Status = status
	if leaseUntil != nil {
		cmd.LeaseUntil = *leaseUntil
	}
	now := time.Now().UTC()
	if !cmd.Leasable(now) {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &cmd, false, nil
	}
	cmd.MarkLeased(owner, ttl, now)
	nextRaw, err := json.Marshal(cmd)
	if err != nil {
		return nil, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE sysbox_agent_commands
SET status=$3, lease_owner=$4, lease_until=$5, attempt=$6, data=$7::jsonb, updated_at=now()
WHERE agent_id=$1
  AND id=$2
  AND status IN ('queued','delivered','')
  AND (lease_until IS NULL OR lease_until <= $8)`,
		agentID, commandID, cmd.Status, cmd.LeaseOwner, cmd.LeaseUntil, cmd.Attempt, string(nextRaw), now)
	if err != nil {
		return nil, false, fmt.Errorf("postgres acquire agent command lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &cmd, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &cmd, true, nil
}

func (s *postgresAPIStore) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	query := `SELECT data::text FROM sysbox_agent_commands ORDER BY updated_at DESC LIMIT 512`
	args := []any{}
	if agentID != "" {
		query = `SELECT data::text FROM sysbox_agent_commands WHERE agent_id=$1 ORDER BY updated_at DESC LIMIT 512`
		args = append(args, agentID)
	}
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres list agent commands: %w", err)
	}
	defer rows.Close()
	var out []controlplane.AgentCommand
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var cmd controlplane.AgentCommand
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return nil, err
		}
		out = append(out, cmd)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) SaveAgentInventory(ctx context.Context, inv controlplane.AgentInventory) error {
	if inv.AgentID == "" {
		inv.AgentID = "_unknown"
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_agent_inventory (agent_id, data, updated_at)
VALUES ($1, $2::jsonb, now())
ON CONFLICT (agent_id) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`,
		inv.AgentID, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save agent inventory: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) GetAgentInventory(ctx context.Context, agentID string) (*controlplane.AgentInventory, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_agent_inventory WHERE agent_id=$1`, agentID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("agent inventory not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get agent inventory: %w", err)
	}
	var inv controlplane.AgentInventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, err
	}
	return &inv, nil
}
