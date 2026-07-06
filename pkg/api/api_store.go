package api

import (
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

func markInterruptedRuns(runs []controlplane.Run) []controlplane.Run {
	for i := range runs {
		if runs[i].Status == controlplane.RunAssigned || runs[i].Status == controlplane.RunRunning {
			runs[i].Status = controlplane.RunFailed
			runs[i].Err = "server restarted before run completion"
			runs[i].Recoverable = true
			if runs[i].EndedAt.IsZero() {
				runs[i].EndedAt = time.Now().UTC()
			}
		}
	}
	return runs
}
