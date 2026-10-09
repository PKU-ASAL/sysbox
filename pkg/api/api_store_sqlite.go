package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

// sqliteAPIStore implements apiStore backed by a local SQLite database.
// It replaces localAPIStore (JSONL append) as the default for local
// deployments: all write-modify-write sequences that were TOCTOU-racy in
// the JSONL implementation (ClaimRun, AcquireAgentCommandLease) now use
// SQLite transactions, matching the Postgres correctness guarantees.
type sqliteAPIStore struct {
	openMu  sync.Mutex
	dbPath  string
	runsDir string // used for HCL file paths, not storage
	db      *sql.DB
}

func (s *sqliteAPIStore) open() (*sql.DB, error) {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	dir := filepath.Dir(s.dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sqlite api mkdir: %w", err)
	}
	// Serialize WAL/schema initialization before SQLite opens its first pooled
	// connection. busy_timeout does not serialize competing journal_mode PRAGMA.
	initLock := flock.New(s.dbPath + ".init.lock")
	defer initLock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, err := initLock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ctx.Err()
	}
	db, err := sql.Open("sqlite", s.dbPath+"?_pragma=busy_timeout%3d5000&_pragma=journal_mode%28WAL%29")
	if err != nil {
		return nil, fmt.Errorf("sqlite api open: %w", err)
	}
	if err := s.ensureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *sqliteAPIStore) ensureSchema(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	schema := sqliteSchemaConnection{conn}
	var marker int
	if err := schema.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sysbox_api_schema_migrations'`).Scan(&marker); err != nil {
		return err
	}
	if marker != 0 {
		var records, version int
		if err := schema.QueryRow(`SELECT count(*), coalesce(max(CASE WHEN name='api' THEN version END),0) FROM sysbox_api_schema_migrations`).Scan(&records, &version); err != nil {
			return fmt.Errorf("%w: SQLite API schema marker requires explicit rebuild: %v", persistence.ErrUnsupportedVersion, err)
		}
		if records != 1 || version != apiSchemaVersion {
			return fmt.Errorf("%w: SQLite API schema %d requires explicit rebuild (current %d)", persistence.ErrUnsupportedVersion, version, apiSchemaVersion)
		}
		if err := s.validateSchema(schema); err != nil {
			return err
		}
	} else {
		// Shared lifecycle repository tables are independent; only API tables
		// make an unversioned database unsupported.
		for _, table := range sqliteAPITables {
			var exists int
			if err := schema.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, table).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				return fmt.Errorf("%w: unversioned SQLite API table %s requires explicit rebuild", persistence.ErrUnsupportedVersion, table)
			}
		}
		if err := s.createSchema(schema); err != nil {
			return err
		}
		if _, err := schema.Exec(`CREATE TABLE sysbox_api_schema_migrations(name TEXT PRIMARY KEY,version INTEGER NOT NULL) STRICT`); err != nil {
			return err
		}
		if _, err := schema.Exec(`INSERT INTO sysbox_api_schema_migrations VALUES('api',?)`, apiSchemaVersion); err != nil {
			return err
		}
	}
	_, err = schema.Exec("COMMIT")
	return err
}

// Keep every schema query on the connection that owns BEGIN IMMEDIATE.
type sqliteSchemaConnection struct{ *sql.Conn }

func (c sqliteSchemaConnection) Exec(query string, args ...any) (sql.Result, error) {
	return c.ExecContext(context.Background(), query, args...)
}
func (c sqliteSchemaConnection) QueryRow(query string, args ...any) *sql.Row {
	return c.QueryRowContext(context.Background(), query, args...)
}

var sqliteAPITables = []string{
	"sysbox_runs", "sysbox_agents", "sysbox_agent_commands", "sysbox_agent_command_events",
	"sysbox_dispatch_retries", "sysbox_console_sessions", "sysbox_node_operations",
	"sysbox_guest_executions", "sysbox_guest_file_operations", "sysbox_run_requests", "sysbox_checkpoints",
	"sysbox_execution_authority",
	"sysbox_health", "sysbox_global_revisions", "sysbox_topology_placements", "sysbox_plans",
}

func (s *sqliteAPIStore) createSchema(db sqliteSchemaConnection) error {
	_, err := db.Exec(`
	CREATE TABLE sysbox_runs (
		id          TEXT NOT NULL,
		topology    TEXT NOT NULL DEFAULT 'default',
		operation   TEXT DEFAULT '',
		op          TEXT DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'queued',
		error       TEXT DEFAULT '',
		parent_id   TEXT DEFAULT '',
		revision    TEXT DEFAULT '',
		target      TEXT DEFAULT '',
		agent_id    TEXT DEFAULT '',
		recoverable INTEGER DEFAULT 0,
		unsafe_state INTEGER DEFAULT 0,
		operation_key TEXT DEFAULT '',
		request_fingerprint TEXT DEFAULT '',
		protocol    TEXT DEFAULT '',
		lease_owner TEXT DEFAULT '',
		lease_until TEXT DEFAULT '',
		attempt     INTEGER NOT NULL DEFAULT 0,
		queued_at   TEXT DEFAULT '',
		assigned_at TEXT DEFAULT '',
		started_at  TEXT DEFAULT '',
		ended_at    TEXT DEFAULT '',
		deadline_at TEXT DEFAULT '',
		snapshot_path TEXT NOT NULL DEFAULT '',
		record_json BLOB NOT NULL DEFAULT X''
	) STRICT;

	CREATE TABLE sysbox_agents (
		id             TEXT PRIMARY KEY,
		name           TEXT DEFAULT '',
		status         TEXT NOT NULL DEFAULT 'online',
		disabled       INTEGER DEFAULT 0,
		quarantined    INTEGER DEFAULT 0,
		reason         TEXT DEFAULT '',
		auth_secret    TEXT DEFAULT '',
		secret_hash    TEXT DEFAULT '',
		protocol       TEXT DEFAULT '',
		capabilities   TEXT DEFAULT '[]',
		labels         TEXT DEFAULT '{}',
		version        TEXT DEFAULT '',
		last_heartbeat TEXT DEFAULT '',
		created_at     TEXT DEFAULT '',
		updated_at     TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE sysbox_agent_commands (
		id         TEXT PRIMARY KEY,
		agent_id   TEXT NOT NULL DEFAULT '',
		type       TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT '',
		error      TEXT DEFAULT '',
		protocol   TEXT DEFAULT '',
		run_payload      BLOB,
		session_payload  BLOB,
		operation_payload BLOB,
		execution_payload BLOB,
		request_payload  BLOB,
		lease_owner TEXT DEFAULT '',
		lease_until TEXT DEFAULT '',
		attempt    INTEGER NOT NULL DEFAULT 0,
		created_at TEXT DEFAULT '',
		delivered  TEXT DEFAULT '',
		acked_at   TEXT DEFAULT '',
		ended_at   TEXT DEFAULT '',
		record_json BLOB NOT NULL DEFAULT X''
	) STRICT;

	CREATE TABLE sysbox_agent_command_events (
		command_id TEXT NOT NULL DEFAULT '',
		type       TEXT NOT NULL DEFAULT '',
		agent_id   TEXT NOT NULL DEFAULT '',
		status     TEXT DEFAULT '',
		message    TEXT DEFAULT '',
		error      TEXT DEFAULT '',
		created_at TEXT DEFAULT '',
		record_json BLOB NOT NULL DEFAULT X''
	) STRICT;

	CREATE TABLE sysbox_dispatch_retries (
		command_id TEXT PRIMARY KEY,
		failures INTEGER NOT NULL DEFAULT 0,
		next TEXT NOT NULL DEFAULT '',
		last_error TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE sysbox_console_sessions (
		id TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;

	CREATE TABLE sysbox_node_operations (
		id TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;


	CREATE TABLE sysbox_guest_executions (
		id   TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;
	CREATE TABLE sysbox_execution_authority (
		id TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;

	CREATE TABLE sysbox_guest_file_operations (
		id   TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;

	CREATE TABLE sysbox_run_requests (
		id          TEXT PRIMARY KEY,
		fingerprint TEXT NOT NULL,
		run_id      TEXT NOT NULL
	) STRICT;

	CREATE TABLE sysbox_checkpoints (
		topology TEXT NOT NULL DEFAULT '',
		run_id   TEXT NOT NULL,
		data     BLOB NOT NULL,
		PRIMARY KEY (topology, run_id)
	) STRICT;

	CREATE TABLE sysbox_health (
		topology TEXT PRIMARY KEY,
		data     BLOB NOT NULL
	) STRICT;


	CREATE TABLE sysbox_global_revisions (
		revision   TEXT PRIMARY KEY,
		files      TEXT NOT NULL DEFAULT '',
		size       INTEGER DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT ''
	) STRICT;

	CREATE TABLE sysbox_topology_placements (
		topology TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		protocol TEXT NOT NULL DEFAULT '',
		bound_at TEXT NOT NULL
	) STRICT;

	CREATE TABLE sysbox_plans (
		id           TEXT NOT NULL DEFAULT '',
		workspace    TEXT NOT NULL DEFAULT '',
		revision     TEXT DEFAULT '',
		state_serial INTEGER DEFAULT 0,
		status       TEXT NOT NULL DEFAULT '',
		summary      TEXT DEFAULT '',
		actions      BLOB,
		created_at   TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (id, workspace)
	) STRICT;

	`)
	if err != nil {
		return err
	}
	return s.validateSchema(db)
}

func (s *sqliteAPIStore) validateSchema(db sqliteSchemaConnection) error {
	for _, table := range sqliteAPITables {
		if _, err := db.Exec("SELECT * FROM " + table + " LIMIT 0"); err != nil {
			return fmt.Errorf("%w: SQLite API table %s requires explicit rebuild: %v", persistence.ErrUnsupportedVersion, table, err)
		}
	}
	if _, err := db.Exec("SELECT data FROM sysbox_node_operations LIMIT 0"); err != nil {
		return fmt.Errorf("%w: node operation schema requires rebuild: %v", persistence.ErrUnsupportedVersion, err)
	}
	var obsoleteUnique int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_index_list('sysbox_run_requests') WHERE "unique"=1 AND origin='u'`).Scan(&obsoleteUnique); err != nil {
		return err
	}
	if obsoleteUnique > 0 {
		return fmt.Errorf("%w: run dispatch schema requires rebuild: run_id must allow multiple execution incarnations", persistence.ErrUnsupportedVersion)
	}
	for _, query := range []string{
		"SELECT data FROM sysbox_console_sessions LIMIT 0",
		"SELECT deadline_at, snapshot_path, record_json FROM sysbox_runs LIMIT 0",
		"SELECT record_json FROM sysbox_agent_commands LIMIT 0",
		"SELECT record_json FROM sysbox_agent_command_events LIMIT 0",
		"SELECT files FROM sysbox_global_revisions LIMIT 0",
	} {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("%w: SQLite API schema requires explicit rebuild: %v", persistence.ErrUnsupportedVersion, err)
		}
	}
	return nil
}

func (s *sqliteAPIStore) GetTopologyPlacement(ctx context.Context, topology string) (*topologyPlacement, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var p topologyPlacement
	var bound string
	err = db.QueryRowContext(ctx, `SELECT topology, agent_id, protocol, bound_at FROM sysbox_topology_placements WHERE topology=?`, topology).Scan(&p.Topology, &p.AgentID, &p.Protocol, &bound)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.BoundAt, err = time.Parse(time.RFC3339Nano, bound)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *sqliteAPIStore) DeleteTopologyPlacement(ctx context.Context, topology string) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM sysbox_topology_placements WHERE topology=?`, topology)
	return err
}

func (s *sqliteAPIStore) SaveTopologyPlacement(ctx context.Context, p topologyPlacement) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO sysbox_topology_placements(topology,agent_id,protocol,bound_at) VALUES(?,?,?,?) ON CONFLICT(topology) DO NOTHING`, p.Topology, p.AgentID, p.Protocol, p.BoundAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var owner string
	if err := db.QueryRowContext(ctx, `SELECT agent_id FROM sysbox_topology_placements WHERE topology=?`, p.Topology).Scan(&owner); err != nil {
		return err
	}
	if owner != p.AgentID {
		return fmt.Errorf("topology %q is already placed on agent %q", p.Topology, owner)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func parseSQLiteTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t
	}
	t, err = time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t.In(time.UTC)
	}
	return time.Time{}
}

func formatSQLiteTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// ── apiStore interface ───────────────────────────────────────────────────────

func (s *sqliteAPIStore) SchemaVersion(context.Context) (int, error) {
	return apiSchemaVersion, nil
}

func (s *sqliteAPIStore) LoadRuns(ctx context.Context) ([]controlplane.Run, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+sqliteRunColumns+" FROM sysbox_runs ORDER BY id,rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []controlplane.Run
	for rows.Next() {
		run, err := scanSQLiteRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

func (s *sqliteAPIStore) SaveRun(ctx context.Context, run controlplane.Run) error {
	return persistence.NewRunRepository(sqliteRunStorage{store: s}).SaveRun(ctx, run)
}

func (s *sqliteAPIStore) GetRun(ctx context.Context, id string) (*controlplane.Run, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	return scanSQLiteRun(db.QueryRowContext(ctx, "SELECT "+sqliteRunColumns+" FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1", id))
}

func (s *sqliteAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(sqliteRunStorage{store: s}).ClaimRun(ctx, runID, agentID, owner, ttl)
}

func (s *sqliteAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(sqliteRunStorage{store: s}).RenewRunLease(ctx, runID, agentID, owner, ttl)
}

func (s *sqliteAPIStore) SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error {
	data, _ := json.Marshal(checkpoint)
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_checkpoints (topology, run_id, data) VALUES (?, ?, ?) ON CONFLICT(topology, run_id) DO UPDATE SET data=excluded.data`,
		topology, runID, data)
	return err
}

func (s *sqliteAPIStore) LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var data []byte
	err = db.QueryRowContext(ctx, `SELECT data FROM sysbox_checkpoints WHERE topology=? AND run_id=?`, topology, runID).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (s *sqliteAPIStore) SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error {
	data, _ := json.Marshal(snap)
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_health (topology, data) VALUES (?, ?) ON CONFLICT(topology) DO UPDATE SET data=excluded.data`,
		topology, data)
	return err
}

func (s *sqliteAPIStore) DeleteHealth(ctx context.Context, topology string) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM sysbox_health WHERE topology=?`, topology)
	return err
}

func (s *sqliteAPIStore) LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var data []byte
	err = db.QueryRowContext(ctx, `SELECT data FROM sysbox_health WHERE topology=?`, topology).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap HealthSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (s *sqliteAPIStore) SaveGlobalRevision(ctx context.Context, rev controlplane.GlobalRevision) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	files, err := json.Marshal(rev.Files)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_global_revisions (revision, files, size, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(revision) DO UPDATE SET files=excluded.files, size=excluded.size, created_at=excluded.created_at`,
		rev.Revision, string(files), rev.Size, rev.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *sqliteAPIStore) GetGlobalRevision(ctx context.Context, revision string) (*controlplane.GlobalRevision, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var rev controlplane.GlobalRevision
	var files []byte
	var createdAt string
	err = db.QueryRowContext(ctx,
		`SELECT revision, files, size, created_at FROM sysbox_global_revisions WHERE revision=?`, revision).
		Scan(&rev.Revision, &files, &rev.Size, &createdAt)
	if err == sql.ErrNoRows {
		return nil, errGlobalRevisionNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: global revision %q requires explicit rebuild", persistence.ErrUnsupportedVersion, revision)
	}
	if err := json.Unmarshal(files, &rev.Files); err != nil {
		return nil, err
	}
	rev.CreatedAt = parseSQLiteTime(createdAt)
	return &rev, nil
}

// ── Plans / Policies ─────────────────────────────────────────────────────────

func (s *sqliteAPIStore) SavePolicy(ctx context.Context, policy controlplane.Policy) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_plans (id, workspace, status, actions, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(id, workspace) DO UPDATE SET status=excluded.status, actions=excluded.actions`,
		"policy:"+policy.ID, policy.Workspace, policy.Mode, []byte(policy.Description), policy.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *sqliteAPIStore) ListPolicies(ctx context.Context, workspace string) ([]controlplane.Policy, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, workspace, status, actions, created_at FROM sysbox_plans WHERE workspace=? AND id LIKE 'policy:%'`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.Policy
	for rows.Next() {
		var pol controlplane.Policy
		var id string
		var actions []byte
		var createdAt string
		if err := rows.Scan(&id, &pol.Workspace, &pol.Mode, &actions, &createdAt); err != nil {
			return nil, err
		}
		pol.ID = strings.TrimPrefix(id, "policy:")
		pol.Description = string(actions)
		pol.CreatedAt = parseSQLiteTime(createdAt)
		out = append(out, pol)
	}
	return out, rows.Err()
}

// ── Console sessions ─────────────────────────────────────────────────────────

// ── Node operations ──────────────────────────────────────────────────────────

func (s *sqliteAPIStore) CreateNodeOperation(ctx context.Context, op controlplane.NodeOperation) error {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return repo.Create(ctx, op)
}
func (s *sqliteAPIStore) CompareAndSwapNodeOperation(ctx context.Context, old, next controlplane.NodeOperation) error {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return repo.CompareAndSwap(ctx, old, next)
}

func (s *sqliteAPIStore) GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.Get(ctx, id)
}

func (s *sqliteAPIStore) ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.List(ctx, workspace)
}

// ── Agents / Commands / Inventory ────────────────────────────────────────────

func (s *sqliteAPIStore) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	raw, err := persistence.EncodeCommandEvent(event)
	if err != nil {
		return err
	}
	normalized, err := persistence.DecodeCommandEvent(raw)
	if err != nil {
		return err
	}
	event = *normalized
	_, err = db.ExecContext(ctx, `INSERT INTO sysbox_agent_command_events (command_id, type, agent_id, status, message, error, created_at, record_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event.CommandID, event.Type, event.AgentID, event.Status, event.Message, event.Error, formatSQLiteTime(event.CreatedAt), raw)
	return err
}

func (s *sqliteAPIStore) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT command_id, type, agent_id, status, message, error, created_at, record_json FROM sysbox_agent_command_events WHERE ?='' OR agent_id=?`, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.AgentCommandEvent
	for rows.Next() {
		var ev controlplane.AgentCommandEvent
		var createdAt string
		var full []byte
		if err := rows.Scan(&ev.CommandID, &ev.Type, &ev.AgentID, &ev.Status, &ev.Message, &ev.Error, &createdAt, &full); err != nil {
			return nil, err
		}
		if len(full) > 0 {
			decoded, err := persistence.DecodeCommandEvent(full)
			if err != nil {
				return nil, err
			}
			if ev.CommandID != decoded.CommandID || ev.Type != decoded.Type || ev.AgentID != decoded.AgentID ||
				ev.Status != decoded.Status || ev.Message != decoded.Message || ev.Error != decoded.Error ||
				createdAt != formatSQLiteTime(decoded.CreatedAt) {
				return nil, fmt.Errorf("%w: inconsistent command event snapshot", persistence.ErrCorruptRecord)
			}
			ev = *decoded
		} else {
			return nil, fmt.Errorf("%w: command event %q requires explicit rebuild", persistence.ErrUnsupportedVersion, ev.CommandID)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	return persistence.NewCommandRepository(sqliteCommandStorage{store: s}).SaveAgentCommand(ctx, cmd)
}
func (s *sqliteAPIStore) DeleteAgentCommand(ctx context.Context, agentID, commandID string) error {
	return persistence.NewCommandRepository(sqliteCommandStorage{store: s}).DeleteAgentCommand(ctx, agentID, commandID)
}
func (s *sqliteAPIStore) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(sqliteCommandStorage{store: s}).ListAgentCommands(ctx, agentID)
}
func (s *sqliteAPIStore) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	return persistence.NewCommandRepository(sqliteCommandStorage{store: s}).AcquireAgentCommandLease(ctx, agentID, commandID, owner, ttl)
}
