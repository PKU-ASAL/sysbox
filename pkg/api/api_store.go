package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

type localAPIStore struct {
	runsDir string
}

const apiSchemaVersion = 9

// errGlobalRevisionNotFound is returned by GetGlobalRevision when no revision
// exists for the requested digest. Callers use errors.Is to detect it so they
// never have to string-match backend-specific messages.
var errGlobalRevisionNotFound = errors.New("global revision not found")

// API schemas are created fresh or validated at the exact current version.
// There is deliberately no in-place migration path.
const apiPostgresSchema = `
CREATE TABLE sysbox_execution_authority (
 id TEXT PRIMARY KEY,
 data JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_api_schema_migrations (
 name TEXT PRIMARY KEY,
 version INTEGER NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_runs (
  topology TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  attempt INTEGER NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_checkpoints (
  topology TEXT NOT NULL,
  run_id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_health (
  topology TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_policies (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_console_sessions (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_node_operations (
  workspace TEXT NOT NULL DEFAULT '',
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_agents (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  disabled BOOLEAN NOT NULL DEFAULT false,
  quarantined BOOLEAN NOT NULL DEFAULT false,
  protocol TEXT NOT NULL DEFAULT '',
  secret_hash TEXT NOT NULL DEFAULT '',
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_agent_command_events (
  agent_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  status TEXT NOT NULL,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_agent_commands (
  agent_id TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  data JSONB NOT NULL,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  attempt INTEGER NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_dispatch_retries (
 command_id TEXT PRIMARY KEY,
 failures INTEGER NOT NULL DEFAULT 0,
 next TIMESTAMPTZ NOT NULL,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE sysbox_guest_executions (
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_guest_file_operations (
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_run_requests (
  id TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  run_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_global_revisions (
  workspace TEXT NOT NULL DEFAULT '',
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sysbox_topology_placements (
  topology TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  protocol TEXT NOT NULL DEFAULT '',
  bound_at TIMESTAMPTZ NOT NULL
);`

func (s *localAPIStore) SchemaVersion(context.Context) (int, error) {
	return apiSchemaVersion, nil
}

func newAPIStore(runsDir, backendURL string) (apiStore, error) {
	if strings.HasPrefix(backendURL, "postgres://") || strings.HasPrefix(backendURL, "postgresql://") {
		store := &postgresAPIStore{dsn: backendURL}
		if err := store.ping(); err != nil {
			return nil, err
		}
		return store, nil
	}
	if strings.HasPrefix(backendURL, "sqlite://") {
		path, err := persistence.SQLitePath(backendURL)
		if err != nil {
			return nil, err
		}
		return &sqliteAPIStore{dbPath: path, runsDir: runsDir}, nil
	}
	return &localAPIStore{runsDir: runsDir}, nil
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

func (s *localAPIStore) DeleteHealth(_ context.Context, topology string) error {
	path := filepath.Join(s.runsDir, topology, "health.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

func (s *localAPIStore) SaveGlobalRevision(_ context.Context, rev controlplane.GlobalRevision) error {
	return writeLocalObject(filepath.Join(s.runsDir, "global-revisions", rev.Revision+".json"), rev)
}

func (s *localAPIStore) GetGlobalRevision(_ context.Context, revision string) (*controlplane.GlobalRevision, error) {
	rev, err := readLocalObject[controlplane.GlobalRevision](filepath.Join(s.runsDir, "global-revisions", revision+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errGlobalRevisionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
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

func (s *localAPIStore) CreateNodeOperation(ctx context.Context, op controlplane.NodeOperation) error {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return repo.Create(ctx, op)
}
func (s *localAPIStore) CompareAndSwapNodeOperation(ctx context.Context, old, next controlplane.NodeOperation) error {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return repo.CompareAndSwap(ctx, old, next)
}

func (s *localAPIStore) GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.Get(ctx, id)
}

func (s *localAPIStore) ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.List(ctx, workspace)
}

// postgresReachabilityTimeout bounds the eager startup check that verifies a
// configured Postgres backend is reachable before the server begins serving.
const postgresReachabilityTimeout = 5 * time.Second

type postgresAPIStore struct {
	dsn string

	mu   sync.Mutex
	pool *pgxpool.Pool
}

// ping verifies the configured Postgres backend is reachable using a fresh
// connection, independent of the lazily-initialized pool. A misconfigured or
// unreachable DSN therefore fails server startup immediately instead of at
// first use.
func (s *postgresAPIStore) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), postgresReachabilityTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsnWithoutSysboxQuery(s.dsn))
	if err != nil {
		return fmt.Errorf("postgres state backend unreachable: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("postgres state backend ping: %w", err)
	}
	return nil
}

// connect acquires a connection from the lazily-initialized pool. The pool and
// its schema are established exactly once; opening a fresh pgx.Conn
// and validating the schema on every request would exhaust the
// server's Postgres connection limit and make every call pay DDL contention.
func (s *postgresAPIStore) connect(ctx context.Context) (*pgxpool.Conn, error) {
	pool, err := s.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres acquire: %w", err)
	}
	return conn, nil
}

func (s *postgresAPIStore) poolFor(ctx context.Context) (*pgxpool.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pool != nil {
		return s.pool, nil
	}
	cfg, err := pgxpool.ParseConfig(dsnWithoutSysboxQuery(s.dsn))
	if err != nil {
		return nil, fmt.Errorf("postgres parse config: %w", err)
	}
	cfg.MaxConns = 32
	cfg.MinConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	if err := s.ensureSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	s.pool = pool
	return s.pool, nil
}

func (s *postgresAPIStore) ensureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()), hashtext('sysbox-api-schema'))`); err != nil {
		return err
	}
	var marker bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname=current_schema() AND tablename='sysbox_api_schema_migrations')`).Scan(&marker); err != nil {
		return err
	}
	if marker {
		var version, records int
		if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(max(version) FILTER (WHERE name='api'),0) FROM sysbox_api_schema_migrations`).Scan(&records, &version); err != nil {
			return fmt.Errorf("%w: API schema marker requires explicit rebuild: %v", persistence.ErrUnsupportedVersion, err)
		}
		if records != 1 || version != apiSchemaVersion {
			return fmt.Errorf("%w: API schema %d requires explicit rebuild (current %d)", persistence.ErrUnsupportedVersion, version, apiSchemaVersion)
		}
		for _, shape := range apiPostgresTableShapes {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname=current_schema() AND tablename=$1)`, shape.table).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: missing API table %s requires explicit rebuild", persistence.ErrUnsupportedVersion, shape.table)
			}
			if _, err := tx.Exec(ctx, "SELECT "+shape.columns+" FROM "+shape.table+" LIMIT 0"); err != nil {
				return fmt.Errorf("%w: API table %s requires explicit rebuild: %v", persistence.ErrUnsupportedVersion, shape.table, err)
			}
		}
		return tx.Commit(ctx)
	}
	// Repository state tables may already coexist in this schema. Only API
	// tables participate in this gate; none may predate the API version marker.
	tables := make([]string, 0, len(apiPostgresTableShapes))
	for _, shape := range apiPostgresTableShapes {
		tables = append(tables, shape.table)
	}
	var existing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname=current_schema() AND tablename=ANY($1))`, tables).Scan(&existing); err != nil {
		return err
	}
	if existing {
		return fmt.Errorf("%w: unversioned API tables require explicit rebuild", persistence.ErrUnsupportedVersion)
	}
	if _, err := tx.Exec(ctx, apiPostgresSchema); err != nil {
		return fmt.Errorf("postgres create API schema: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sysbox_api_schema_migrations(name,version) VALUES('api',$1)`, apiSchemaVersion); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var apiPostgresTableShapes = []struct{ table, columns string }{
	{"sysbox_runs", "topology,id,data,status,agent_id,lease_owner,lease_until,attempt,updated_at"},
	{"sysbox_checkpoints", "topology,run_id,data,updated_at"},
	{"sysbox_health", "topology,data,updated_at"},
	{"sysbox_policies", "workspace,id,data,created_at"},
	{"sysbox_console_sessions", "workspace,id,data,updated_at"},
	{"sysbox_node_operations", "workspace,id,data,updated_at"},
	{"sysbox_agents", "id,status,disabled,quarantined,protocol,secret_hash,data,updated_at"},
	{"sysbox_agent_command_events", "agent_id,command_id,status,data,created_at"},
	{"sysbox_agent_commands", "agent_id,id,status,data,lease_owner,lease_until,attempt,updated_at"},
	{"sysbox_dispatch_retries", "command_id,failures,next,last_error"},
	{"sysbox_guest_executions", "id,data,updated_at"},
	{"sysbox_guest_file_operations", "id,data,updated_at"},
	{"sysbox_execution_authority", "id,data,updated_at"},
	{"sysbox_run_requests", "id,fingerprint,run_id,created_at"},
	{"sysbox_global_revisions", "workspace,id,data,created_at"},
	{"sysbox_topology_placements", "topology,agent_id,protocol,bound_at"},
}

func (s *postgresAPIStore) SchemaVersion(ctx context.Context) (int, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
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
	defer conn.Release()
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
	defer conn.Release()
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
	defer conn.Release()
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

func (s *postgresAPIStore) DeleteHealth(ctx context.Context, topology string) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `DELETE FROM sysbox_health WHERE topology=$1`, topology); err != nil {
		return fmt.Errorf("postgres delete health: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
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

func (s *postgresAPIStore) SaveGlobalRevision(ctx context.Context, rev controlplane.GlobalRevision) error {
	return s.saveObject(ctx, "sysbox_global_revisions", "", rev.Revision, rev)
}

func (s *postgresAPIStore) GetTopologyPlacement(ctx context.Context, topology string) (*topologyPlacement, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	p := &topologyPlacement{}
	err = conn.QueryRow(ctx, `SELECT topology, agent_id, protocol, bound_at FROM sysbox_topology_placements WHERE topology=$1`, topology).Scan(&p.Topology, &p.AgentID, &p.Protocol, &p.BoundAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *postgresAPIStore) DeleteTopologyPlacement(ctx context.Context, topology string) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, `DELETE FROM sysbox_topology_placements WHERE topology=$1`, topology)
	return err
}

func (s *postgresAPIStore) SaveTopologyPlacement(ctx context.Context, p topologyPlacement) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, `INSERT INTO sysbox_topology_placements(topology,agent_id,protocol,bound_at) VALUES($1,$2,$3,$4) ON CONFLICT(topology) DO NOTHING`, p.Topology, p.AgentID, p.Protocol, p.BoundAt)
	if err != nil {
		return err
	}
	var owner string
	if err := conn.QueryRow(ctx, `SELECT agent_id FROM sysbox_topology_placements WHERE topology=$1`, p.Topology).Scan(&owner); err != nil {
		return err
	}
	if owner != p.AgentID {
		return fmt.Errorf("topology %q is already placed on agent %q", p.Topology, owner)
	}
	return nil
}

func (s *postgresAPIStore) GetGlobalRevision(ctx context.Context, revision string) (*controlplane.GlobalRevision, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_global_revisions WHERE workspace=$1 AND id=$2`, "", revision).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, errGlobalRevisionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get global revision: %w", err)
	}
	var rev controlplane.GlobalRevision
	if err := json.Unmarshal(raw, &rev); err != nil {
		return nil, fmt.Errorf("decode global revision: %w", err)
	}
	return &rev, nil
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
	defer conn.Release()
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
	defer conn.Release()
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
	defer conn.Release()
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

func (s *postgresAPIStore) CreateNodeOperation(ctx context.Context, op controlplane.NodeOperation) error {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return repo.Create(ctx, op)
}
func (s *postgresAPIStore) CompareAndSwapNodeOperation(ctx context.Context, old, next controlplane.NodeOperation) error {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return repo.CompareAndSwap(ctx, old, next)
}

func (s *postgresAPIStore) GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.Get(ctx, id)
}

func (s *postgresAPIStore) ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.List(ctx, workspace)
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
	defer conn.Release()
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
	defer conn.Release()
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

func dsnWithoutSysboxQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	query.Del("topology")
	u.RawQuery = query.Encode()
	return u.String()
}

func markInterruptedRuns(runs []controlplane.Run) []controlplane.Run {
	for i := range runs {
		// Restarting an API process is not evidence that an independently
		// assigned agent stopped. Its durable lease and observations decide that.
		if runs[i].AgentID == "" && (runs[i].Status == controlplane.RunAssigned || runs[i].Status == controlplane.RunRunning) {
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
