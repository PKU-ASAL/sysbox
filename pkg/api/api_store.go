package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

type apiStore interface {
	SchemaVersion(ctx context.Context) (int, error)
	LoadRuns(ctx context.Context) ([]Run, error)
	GetRun(ctx context.Context, id string) (*Run, error)
	SaveRun(ctx context.Context, run Run) error
	ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error)
	RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error)
	SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error
	LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error)
	SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error
	LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error)
	SaveRevision(ctx context.Context, rev controlplane.Revision) error
	ListRevisions(ctx context.Context, workspace string) ([]controlplane.Revision, error)
	GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error)
	SavePlan(ctx context.Context, plan controlplane.Plan) error
	ListPlans(ctx context.Context, workspace string) ([]controlplane.Plan, error)
	GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error)
	SavePolicy(ctx context.Context, policy controlplane.Policy) error
	ListPolicies(ctx context.Context, workspace string) ([]controlplane.Policy, error)
	SaveConsoleSession(ctx context.Context, sess controlplane.ConsoleSession) error
	GetConsoleSession(ctx context.Context, id string) (*controlplane.ConsoleSession, error)
	ListConsoleSessions(ctx context.Context, workspace string) ([]controlplane.ConsoleSession, error)
	SaveNodeOperation(ctx context.Context, op controlplane.NodeOperation) error
	GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error)
	ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error)
	SaveAgent(ctx context.Context, agent controlplane.Agent) error
	GetAgent(ctx context.Context, id string) (*controlplane.Agent, error)
	ListAgents(ctx context.Context) ([]controlplane.Agent, error)
	SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error
	ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error)
	SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error
	ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error)
	AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error)
	SaveAgentInventory(ctx context.Context, inv controlplane.AgentInventory) error
	GetAgentInventory(ctx context.Context, agentID string) (*controlplane.AgentInventory, error)
}

type localAPIStore struct {
	runsDir string
}

const apiSchemaVersion = 3

type apiMigration struct {
	Version int
	Name    string
	SQL     string
}

var apiMigrations = []apiMigration{
	{
		Version: 1,
		Name:    "base_control_plane",
		SQL: `
CREATE TABLE IF NOT EXISTS sysbox_runs (
  topology TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_checkpoints (
  topology TEXT NOT NULL,
  run_id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_health (
  topology TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_revisions (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_plans (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_policies (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_console_sessions (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_node_operations (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`,
	},
	{
		Version: 2,
		Name:    "agent_registry_and_commands",
		SQL: `
CREATE TABLE IF NOT EXISTS sysbox_agents (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  disabled BOOLEAN NOT NULL DEFAULT false,
  quarantined BOOLEAN NOT NULL DEFAULT false,
  protocol TEXT NOT NULL DEFAULT '',
  secret_hash TEXT NOT NULL DEFAULT '',
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_agent_command_events (
  agent_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  status TEXT NOT NULL,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_agent_commands (
  agent_id TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_agent_inventory (
  agent_id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE sysbox_agents ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE sysbox_agents ADD COLUMN IF NOT EXISTS quarantined BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE sysbox_agents ADD COLUMN IF NOT EXISTS protocol TEXT NOT NULL DEFAULT '';
ALTER TABLE sysbox_agents ADD COLUMN IF NOT EXISTS secret_hash TEXT NOT NULL DEFAULT '';`,
	},
	{
		Version: 3,
		Name:    "run_and_command_leases",
		SQL: `
ALTER TABLE sysbox_runs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT '';
ALTER TABLE sysbox_runs ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sysbox_runs ADD COLUMN IF NOT EXISTS lease_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE sysbox_runs ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;
ALTER TABLE sysbox_runs ADD COLUMN IF NOT EXISTS attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sysbox_agent_commands ADD COLUMN IF NOT EXISTS lease_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE sysbox_agent_commands ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;
ALTER TABLE sysbox_agent_commands ADD COLUMN IF NOT EXISTS attempt INTEGER NOT NULL DEFAULT 0;`,
	},
}

func (s *localAPIStore) SchemaVersion(context.Context) (int, error) {
	return apiSchemaVersion, nil
}

func newAPIStore(runsDir, backendURL string) apiStore {
	if strings.HasPrefix(backendURL, "postgres://") || strings.HasPrefix(backendURL, "postgresql://") {
		return &postgresAPIStore{dsn: backendURL}
	}
	if strings.HasPrefix(backendURL, "sqlite://") {
		path := strings.TrimPrefix(backendURL, "sqlite://")
		return &sqliteAPIStore{dbPath: path, runsDir: runsDir}
	}
	return &localAPIStore{runsDir: runsDir}
}

func (s *localAPIStore) LoadRuns(_ context.Context) ([]Run, error) {
	var out []Run
	pattern := filepath.Join(s.runsDir, "*", "runs.jsonl")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			var r Run
			if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
				out = append(out, r)
			}
		}
		fh.Close()
	}
	return out, nil
}

func (s *localAPIStore) GetRun(ctx context.Context, id string) (*Run, error) {
	runs, err := s.LoadRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range latestRunsByID(runs) {
		if run.ID == id {
			normalizeRunProductFields(&run)
			return &run, nil
		}
	}
	return nil, fmt.Errorf("run not found")
}

func (s *localAPIStore) SaveRun(_ context.Context, run Run) error {
	normalizeRunProductFields(&run)
	dir := filepath.Join(s.runsDir, run.Topology)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "runs.jsonl")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	return json.NewEncoder(fh).Encode(run)
}

func (s *localAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	if !runLeasable(*run, agentID, time.Now().UTC()) {
		return run, false, nil
	}
	now := time.Now().UTC()
	run.Status = RunRunning
	run.LeaseOwner = owner
	run.LeaseUntil = now.Add(ttl)
	run.Attempt++
	if run.StartedAt.IsZero() || run.StartedAt.Equal(run.QueuedAt) {
		run.StartedAt = now
	}
	if err := s.SaveRun(ctx, *run); err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (s *localAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	if run.AgentID != agentID || run.Status != RunRunning || run.LeaseOwner != owner {
		return run, false, nil
	}
	run.LeaseUntil = time.Now().UTC().Add(ttl)
	if err := s.SaveRun(ctx, *run); err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (s *localAPIStore) SaveCheckpoint(_ context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error {
	return runtime.WriteCheckpoint(s.checkpointFile(topology, runID), checkpoint)
}

func (s *localAPIStore) LoadCheckpoint(_ context.Context, topology, runID string) (*runtime.OperationCheckpoint, error) {
	raw, err := os.ReadFile(s.checkpointFile(topology, runID))
	if err != nil {
		return nil, fmt.Errorf("checkpoint not found")
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return &cp, nil
}

func (s *localAPIStore) SaveHealth(_ context.Context, topology string, snap HealthSnapshot) error {
	path := filepath.Join(s.runsDir, topology, "health.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (s *localAPIStore) LoadHealth(_ context.Context, topology string) (*HealthSnapshot, error) {
	raw, err := os.ReadFile(filepath.Join(s.runsDir, topology, "health.json"))
	if err != nil {
		return nil, fmt.Errorf("health snapshot not found")
	}
	var snap HealthSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode health snapshot: %w", err)
	}
	return &snap, nil
}

func (s *localAPIStore) checkpointFile(topology, runID string) string {
	return filepath.Join(s.runsDir, topology, "runs", runID+".checkpoint.json")
}

func (s *localAPIStore) SaveRevision(_ context.Context, rev controlplane.Revision) error {
	return writeLocalObject(filepath.Join(s.runsDir, rev.Workspace, "revisions", rev.ID+".json"), rev)
}

func (s *localAPIStore) ListRevisions(_ context.Context, workspace string) ([]controlplane.Revision, error) {
	return readLocalObjects[controlplane.Revision](filepath.Join(s.runsDir, workspace, "revisions", "*.json"))
}

func (s *localAPIStore) GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error) {
	items, err := s.ListRevisions(ctx, workspace)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == revisionID {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("revision not found")
}

func (s *localAPIStore) SavePlan(_ context.Context, plan controlplane.Plan) error {
	return writeLocalObject(filepath.Join(s.runsDir, plan.Workspace, "plans", plan.ID+".json"), plan)
}

func (s *localAPIStore) ListPlans(_ context.Context, workspace string) ([]controlplane.Plan, error) {
	return readLocalObjects[controlplane.Plan](filepath.Join(s.runsDir, workspace, "plans", "*.json"))
}

func (s *localAPIStore) GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error) {
	items, err := s.ListPlans(ctx, workspace)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == planID {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("plan not found")
}

func (s *localAPIStore) SavePolicy(_ context.Context, policy controlplane.Policy) error {
	workspace := policy.Workspace
	if workspace == "" {
		workspace = "_project"
	}
	return writeLocalObject(filepath.Join(s.runsDir, workspace, "policies", policy.ID+".json"), policy)
}

func (s *localAPIStore) ListPolicies(_ context.Context, workspace string) ([]controlplane.Policy, error) {
	if workspace == "" {
		workspace = "_project"
	}
	return readLocalObjects[controlplane.Policy](filepath.Join(s.runsDir, workspace, "policies", "*.json"))
}

func (s *localAPIStore) SaveConsoleSession(_ context.Context, sess controlplane.ConsoleSession) error {
	workspace := sess.Workspace
	if workspace == "" {
		workspace = sess.Topology
	}
	return writeLocalObject(filepath.Join(s.runsDir, workspace, "sessions", sess.ID+".json"), sess)
}

func (s *localAPIStore) GetConsoleSession(ctx context.Context, id string) (*controlplane.ConsoleSession, error) {
	items, err := s.ListConsoleSessions(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("session not found")
}

func (s *localAPIStore) ListConsoleSessions(_ context.Context, workspace string) ([]controlplane.ConsoleSession, error) {
	if workspace == "" {
		return readLocalObjects[controlplane.ConsoleSession](filepath.Join(s.runsDir, "*", "sessions", "*.json"))
	}
	return readLocalObjects[controlplane.ConsoleSession](filepath.Join(s.runsDir, workspace, "sessions", "*.json"))
}

func (s *localAPIStore) SaveNodeOperation(_ context.Context, op controlplane.NodeOperation) error {
	workspace := op.Workspace
	if workspace == "" {
		workspace = op.Topology
	}
	return writeLocalObject(filepath.Join(s.runsDir, workspace, "node-ops", op.ID+".json"), op)
}

func (s *localAPIStore) GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	items, err := s.ListNodeOperations(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("node operation not found")
}

func (s *localAPIStore) ListNodeOperations(_ context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	if workspace == "" {
		return readLocalObjects[controlplane.NodeOperation](filepath.Join(s.runsDir, "*", "node-ops", "*.json"))
	}
	return readLocalObjects[controlplane.NodeOperation](filepath.Join(s.runsDir, workspace, "node-ops", "*.json"))
}

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
		if !agentCommandLeasable(cmd, now) {
			return &cmd, false, nil
		}
		cmd.Status = "leased"
		cmd.LeaseOwner = owner
		cmd.LeaseUntil = now.Add(ttl)
		cmd.Attempt++
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

func writeLocalObject(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func readLocalObjects[T any](pattern string) ([]T, error) {
	files, _ := filepath.Glob(pattern)
	out := make([]T, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var item T
		if err := json.Unmarshal(raw, &item); err == nil {
			out = append(out, item)
		}
	}
	return out, nil
}

type postgresAPIStore struct {
	dsn string
}

func (s *postgresAPIStore) connect(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsnWithoutSysboxQuery(s.dsn))
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	if err := s.ensureSchema(ctx, conn); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return conn, nil
}

func (s *postgresAPIStore) ensureSchema(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
CREATE TABLE IF NOT EXISTS sysbox_api_schema_migrations (
  name TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	if err != nil {
		return fmt.Errorf("postgres ensure schema migrations table: %w", err)
	}
	for _, migration := range apiMigrations {
		if err := s.applyMigration(ctx, conn, migration); err != nil {
			return err
		}
	}
	return nil
}

func (s *postgresAPIStore) applyMigration(ctx context.Context, conn *pgx.Conn, migration apiMigration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, migration.SQL); err != nil {
		return fmt.Errorf("postgres apply api migration %03d_%s: %w", migration.Version, migration.Name, err)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO sysbox_api_schema_migrations (name, version, updated_at)
VALUES ('api', $1, now())
ON CONFLICT (name) DO UPDATE SET version=EXCLUDED.version, updated_at=now()
WHERE sysbox_api_schema_migrations.version < EXCLUDED.version`, migration.Version)
	if err != nil {
		return fmt.Errorf("postgres record api schema version: %w", err)
	}
	stepName := fmt.Sprintf("api/%03d_%s", migration.Version, migration.Name)
	_, err = tx.Exec(ctx, `
INSERT INTO sysbox_api_schema_migrations (name, version, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (name) DO UPDATE SET version=EXCLUDED.version, updated_at=now()`,
		stepName, migration.Version)
	if err != nil {
		return fmt.Errorf("postgres record api migration step: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *postgresAPIStore) SchemaVersion(ctx context.Context) (int, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	var version int
	err = conn.QueryRow(ctx, `SELECT version FROM sysbox_api_schema_migrations WHERE name='api'`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("postgres api schema version: %w", err)
	}
	return version, nil
}

func (s *postgresAPIStore) LoadRuns(ctx context.Context) ([]Run, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT data::text FROM sysbox_runs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres load runs: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var run Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, fmt.Errorf("decode run: %w", err)
		}
		normalizeRunProductFields(&run)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) GetRun(ctx context.Context, id string) (*Run, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get run: %w", err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("decode run: %w", err)
	}
	normalizeRunProductFields(&run)
	return &run, nil
}

func (s *postgresAPIStore) SaveRun(ctx context.Context, run Run) error {
	normalizeRunProductFields(&run)
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_runs (topology, id, status, agent_id, lease_owner, lease_until, attempt, data, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, now())
ON CONFLICT (id) DO UPDATE SET topology=EXCLUDED.topology, status=EXCLUDED.status, agent_id=EXCLUDED.agent_id, lease_owner=EXCLUDED.lease_owner, lease_until=EXCLUDED.lease_until, attempt=EXCLUDED.attempt, data=EXCLUDED.data, updated_at=now()`,
		run.Topology, run.ID, string(run.Status), run.AgentID, run.LeaseOwner, nullableTime(run.LeaseUntil), run.Attempt, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save run: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error) {
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
FROM sysbox_runs
WHERE id=$1
FOR UPDATE`, runID).Scan(&raw, &status, &leaseUntil)
	if err == pgx.ErrNoRows {
		return nil, false, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres load run lease row: %w", err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, false, err
	}
	run.Status = RunStatus(status)
	if leaseUntil != nil {
		run.LeaseUntil = *leaseUntil
	}
	now := time.Now().UTC()
	if !runLeasable(run, agentID, now) {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	run.Status = RunRunning
	run.LeaseOwner = owner
	run.LeaseUntil = now.Add(ttl)
	run.Attempt++
	if run.StartedAt.IsZero() || run.StartedAt.Equal(run.QueuedAt) {
		run.StartedAt = now
	}
	nextRaw, err := json.Marshal(run)
	if err != nil {
		return nil, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE sysbox_runs
SET status=$3, lease_owner=$4, lease_until=$5, attempt=$6, data=$7::jsonb, updated_at=now()
WHERE id=$1
  AND agent_id=$2
  AND status='assigned'
  AND (lease_until IS NULL OR lease_until <= $8)`,
		runID, agentID, string(run.Status), run.LeaseOwner, run.LeaseUntil, run.Attempt, string(nextRaw), now)
	if err != nil {
		return nil, false, fmt.Errorf("postgres claim run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &run, true, nil
}

func (s *postgresAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error) {
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
	err = tx.QueryRow(ctx, `
SELECT data::text
FROM sysbox_runs
WHERE id=$1
FOR UPDATE`, runID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, false, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres load run renew row: %w", err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, false, err
	}
	if run.AgentID != agentID || run.Status != RunRunning || run.LeaseOwner != owner {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	run.LeaseUntil = time.Now().UTC().Add(ttl)
	nextRaw, err := json.Marshal(run)
	if err != nil {
		return nil, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE sysbox_runs
SET lease_until=$4, data=$5::jsonb, updated_at=now()
WHERE id=$1 AND agent_id=$2 AND lease_owner=$3 AND status='running'`,
		runID, agentID, owner, run.LeaseUntil, string(nextRaw))
	if err != nil {
		return nil, false, fmt.Errorf("postgres renew run lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &run, true, nil
}

func (s *postgresAPIStore) SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_checkpoints (topology, run_id, data, updated_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (run_id) DO UPDATE SET topology=EXCLUDED.topology, data=EXCLUDED.data, updated_at=now()`,
		topology, runID, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save checkpoint: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_checkpoints WHERE topology=$1 AND run_id=$2`, topology, runID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("checkpoint not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres load checkpoint: %w", err)
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return &cp, nil
}

func (s *postgresAPIStore) SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_health (topology, data, updated_at)
VALUES ($1, $2::jsonb, now())
ON CONFLICT (topology) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`,
		topology, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save health: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_health WHERE topology=$1`, topology).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("health snapshot not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres load health: %w", err)
	}
	var snap HealthSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode health snapshot: %w", err)
	}
	return &snap, nil
}

func (s *postgresAPIStore) SaveRevision(ctx context.Context, rev controlplane.Revision) error {
	return s.saveObject(ctx, "sysbox_revisions", rev.Workspace, rev.ID, rev)
}

func (s *postgresAPIStore) ListRevisions(ctx context.Context, workspace string) ([]controlplane.Revision, error) {
	return listPostgresObjects[controlplane.Revision](ctx, s, "sysbox_revisions", workspace)
}

func (s *postgresAPIStore) GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error) {
	return getPostgresObject[controlplane.Revision](ctx, s, "sysbox_revisions", workspace, revisionID)
}

func (s *postgresAPIStore) SavePlan(ctx context.Context, plan controlplane.Plan) error {
	return s.saveObject(ctx, "sysbox_plans", plan.Workspace, plan.ID, plan)
}

func (s *postgresAPIStore) ListPlans(ctx context.Context, workspace string) ([]controlplane.Plan, error) {
	return listPostgresObjects[controlplane.Plan](ctx, s, "sysbox_plans", workspace)
}

func (s *postgresAPIStore) GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error) {
	return getPostgresObject[controlplane.Plan](ctx, s, "sysbox_plans", workspace, planID)
}

func (s *postgresAPIStore) SavePolicy(ctx context.Context, policy controlplane.Policy) error {
	workspace := policy.Workspace
	if workspace == "" {
		workspace = "_project"
	}
	return s.saveObject(ctx, "sysbox_policies", workspace, policy.ID, policy)
}

func (s *postgresAPIStore) ListPolicies(ctx context.Context, workspace string) ([]controlplane.Policy, error) {
	if workspace == "" {
		workspace = "_project"
	}
	return listPostgresObjects[controlplane.Policy](ctx, s, "sysbox_policies", workspace)
}

func (s *postgresAPIStore) SaveConsoleSession(ctx context.Context, sess controlplane.ConsoleSession) error {
	workspace := sess.Workspace
	if workspace == "" {
		workspace = sess.Topology
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_console_sessions (workspace, id, data, updated_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (id) DO UPDATE SET workspace=EXCLUDED.workspace, data=EXCLUDED.data, updated_at=now()`,
		workspace, sess.ID, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save console session: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) GetConsoleSession(ctx context.Context, id string) (*controlplane.ConsoleSession, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_console_sessions WHERE id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get console session: %w", err)
	}
	var sess controlplane.ConsoleSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *postgresAPIStore) ListConsoleSessions(ctx context.Context, workspace string) ([]controlplane.ConsoleSession, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	query := `SELECT data::text FROM sysbox_console_sessions ORDER BY updated_at DESC`
	args := []any{}
	if workspace != "" {
		query = `SELECT data::text FROM sysbox_console_sessions WHERE workspace=$1 ORDER BY updated_at DESC`
		args = append(args, workspace)
	}
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres list console sessions: %w", err)
	}
	defer rows.Close()
	var out []controlplane.ConsoleSession
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item controlplane.ConsoleSession
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) SaveNodeOperation(ctx context.Context, op controlplane.NodeOperation) error {
	workspace := op.Workspace
	if workspace == "" {
		workspace = op.Topology
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_node_operations (workspace, id, data, updated_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (id) DO UPDATE SET workspace=EXCLUDED.workspace, data=EXCLUDED.data, updated_at=now()`,
		workspace, op.ID, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save node operation: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_node_operations WHERE id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("node operation not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get node operation: %w", err)
	}
	var op controlplane.NodeOperation
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (s *postgresAPIStore) ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	query := `SELECT data::text FROM sysbox_node_operations ORDER BY updated_at DESC`
	args := []any{}
	if workspace != "" {
		query = `SELECT data::text FROM sysbox_node_operations WHERE workspace=$1 ORDER BY updated_at DESC`
		args = append(args, workspace)
	}
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres list node operations: %w", err)
	}
	defer rows.Close()
	var out []controlplane.NodeOperation
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item controlplane.NodeOperation
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
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
		cmd.Status = "queued"
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
	if !agentCommandLeasable(cmd, now) {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &cmd, false, nil
	}
	cmd.Status = "leased"
	cmd.LeaseOwner = owner
	cmd.LeaseUntil = now.Add(ttl)
	cmd.Attempt++
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

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *postgresAPIStore) saveObject(ctx context.Context, table, workspace, id string, v any) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (workspace, id, data, created_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (id) DO UPDATE SET workspace=EXCLUDED.workspace, data=EXCLUDED.data`, table),
		workspace, id, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save %s: %w", table, err)
	}
	return nil
}

func listPostgresObjects[T any](ctx context.Context, s *postgresAPIStore, table, workspace string) ([]T, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT data::text FROM %s WHERE workspace=$1 ORDER BY created_at DESC`, table), workspace)
	if err != nil {
		return nil, fmt.Errorf("postgres list %s: %w", table, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func getPostgresObject[T any](ctx context.Context, s *postgresAPIStore, table, workspace, id string) (*T, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, fmt.Sprintf(`SELECT data::text FROM %s WHERE workspace=$1 AND id=$2`, table), workspace, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("object not found")
	}
	if err != nil {
		return nil, err
	}
	var item T
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func dsnWithoutSysboxQuery(raw string) string {
	out := raw
	if idx := strings.Index(out, "?"); idx >= 0 {
		out = out[:idx]
	}
	return out
}

func markInterruptedRuns(runs []Run) []Run {
	for i := range runs {
		if runs[i].Status == RunAssigned || runs[i].Status == RunRunning {
			runs[i].Status = RunFailed
			runs[i].Err = "server restarted before run completion"
			runs[i].Recoverable = true
			if runs[i].EndedAt.IsZero() {
				runs[i].EndedAt = time.Now().UTC()
			}
		}
	}
	return runs
}
