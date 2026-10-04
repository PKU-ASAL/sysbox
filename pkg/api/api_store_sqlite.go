package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	dbPath  string
	runsDir string // used for HCL file paths, not storage
	db      *sql.DB
}

func (s *sqliteAPIStore) open() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	dir := filepath.Dir(s.dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sqlite api mkdir: %w", err)
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
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS sysbox_runs (
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
		deadline_at TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_agents (
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

	CREATE TABLE IF NOT EXISTS sysbox_agent_commands (
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
		ended_at   TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_agent_command_events (
		command_id TEXT NOT NULL DEFAULT '',
		type       TEXT NOT NULL DEFAULT '',
		agent_id   TEXT NOT NULL DEFAULT '',
		status     TEXT DEFAULT '',
		message    TEXT DEFAULT '',
		error      TEXT DEFAULT '',
		created_at TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_dispatch_retries (
		command_id TEXT PRIMARY KEY,
		failures INTEGER NOT NULL DEFAULT 0,
		next TEXT NOT NULL DEFAULT '',
		last_error TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_console_sessions (
		id          TEXT PRIMARY KEY,
		topology    TEXT NOT NULL DEFAULT '',
		node        TEXT NOT NULL DEFAULT '',
		agent_id    TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT '',
		error       TEXT DEFAULT '',
		exit_code   INTEGER,
		requested_by TEXT DEFAULT '',
		roles       TEXT DEFAULT '[]',
		policy      TEXT DEFAULT '',
		tty         INTEGER DEFAULT 0,
		audit       TEXT DEFAULT '[]',
		created_at  TEXT NOT NULL DEFAULT '',
		started_at  TEXT DEFAULT '',
		ended_at    TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_node_operations (
		id          TEXT PRIMARY KEY,
		topology    TEXT NOT NULL DEFAULT '',
		operation   TEXT NOT NULL DEFAULT '',
		node        TEXT DEFAULT '',
		agent_id    TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT '',
		error       TEXT DEFAULT '',
		requested_by TEXT DEFAULT '',
		roles       TEXT DEFAULT '[]',
		audit       TEXT DEFAULT '[]',
		created_at  TEXT NOT NULL DEFAULT '',
		started_at  TEXT DEFAULT '',
		ended_at    TEXT DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_agent_inventory (
		agent_id     TEXT PRIMARY KEY,
		capabilities TEXT DEFAULT '[]',
		labels       TEXT DEFAULT '{}',
		resources    TEXT DEFAULT '{}',
		topologies   TEXT DEFAULT '[]',
		artifacts    TEXT DEFAULT '[]',
		tools        TEXT DEFAULT '[]',
		status       TEXT DEFAULT '',
		stale        INTEGER DEFAULT 0,
		observed_at  TEXT NOT NULL DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_guest_executions (
		id   TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_guest_file_operations (
		id   TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_run_requests (
		id          TEXT PRIMARY KEY,
		fingerprint TEXT NOT NULL,
		run_id      TEXT NOT NULL UNIQUE
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_checkpoints (
		topology TEXT NOT NULL DEFAULT '',
		run_id   TEXT NOT NULL,
		data     BLOB NOT NULL,
		PRIMARY KEY (topology, run_id)
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_health (
		topology TEXT PRIMARY KEY,
		data     BLOB NOT NULL
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_projection (
		topology TEXT PRIMARY KEY,
		data     BLOB NOT NULL
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_global_revisions (
		revision   TEXT PRIMARY KEY,
		files      TEXT NOT NULL DEFAULT '',
		size       INTEGER DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT ''
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_topology_placements (
		topology TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		protocol TEXT NOT NULL DEFAULT '',
		bound_at TEXT NOT NULL
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_plans (
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

	PRAGMA journal_mode=WAL;
	PRAGMA foreign_keys=ON;
	`)
	if err != nil {
		return err
	}
	if err := s.ensureRunsColumn(db, "deadline_at", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureRunsColumn(db, "snapshot_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureRunsColumn(db, "record_json", "BLOB NOT NULL DEFAULT X''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "sysbox_agent_commands", "record_json", "BLOB NOT NULL DEFAULT X''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "sysbox_agent_command_events", "record_json", "BLOB NOT NULL DEFAULT X''"); err != nil {
		return err
	}
	if err := s.ensureGlobalRevisionsColumn(db, "files", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
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

// ensureRunsColumn adds a missing column to sysbox_runs for pre-existing
// databases (see ensureColumn).
func (s *sqliteAPIStore) ensureRunsColumn(db *sql.DB, column, def string) error {
	return ensureColumn(db, "sysbox_runs", column, def)
}

// ensureGlobalRevisionsColumn adds a missing column to sysbox_global_revisions
// for pre-existing databases (see ensureColumn).
func (s *sqliteAPIStore) ensureGlobalRevisionsColumn(db *sql.DB, column, def string) error {
	return ensureColumn(db, "sysbox_global_revisions", column, def)
}

// ensureColumn adds a missing column to a table for pre-existing databases.
// sqlite has no versioned migration framework here (only CREATE TABLE IF NOT
// EXISTS), so the ALTER is guarded by a PRAGMA table_info check to stay
// idempotent across restarts. The table and column names are internal
// constants, never user input.
func ensureColumn(db *sql.DB, table, column, def string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}

	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def))
	return err
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

func (s *sqliteAPIStore) SaveResourceProjection(ctx context.Context, proj controlplane.ResourceProjection) error {
	data, _ := json.Marshal(proj)
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_projection (topology, data) VALUES (?, ?) ON CONFLICT(topology) DO UPDATE SET data=excluded.data`,
		proj.Topology, data)
	return err
}

func (s *sqliteAPIStore) LoadResourceProjection(ctx context.Context, topology string) (*controlplane.ResourceProjection, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var data []byte
	err = db.QueryRowContext(ctx, `SELECT data FROM sysbox_projection WHERE topology=?`, topology).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var proj controlplane.ResourceProjection
	if err := json.Unmarshal(data, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
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
	// Legacy rows (written before directory-tree revisions) have an empty files
	// column; they cannot be unmarshaled, so treat them as not-found rather than
	// surfacing a JSON error as a 500 on apply.
	if len(files) == 0 {
		return nil, errGlobalRevisionNotFound
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

func (s *sqliteAPIStore) SaveConsoleSession(ctx context.Context, sess controlplane.ConsoleSession) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	tty := 0
	if sess.TTY {
		tty = 1
	}
	audit, _ := json.Marshal(sess.Audit)
	roles, _ := json.Marshal(sess.Roles)
	exitCode := sql.NullInt64{}
	if sess.ExitCode != nil {
		exitCode.Int64 = int64(*sess.ExitCode)
		exitCode.Valid = true
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_console_sessions (id, topology, node, agent_id, status, error, exit_code, requested_by, roles, policy, tty, audit, created_at, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET status=excluded.status, error=excluded.error, exit_code=excluded.exit_code, ended_at=excluded.ended_at`,
		sess.ID, sess.Topology, sess.Node, sess.AgentID, sess.Status, sess.Err, exitCode, sess.RequestedBy, string(roles), sess.Policy, tty, string(audit),
		formatSQLiteTime(sess.CreatedAt), formatSQLiteTime(sess.StartedAt), formatSQLiteTime(sess.EndedAt))
	return err
}

func (s *sqliteAPIStore) GetConsoleSession(ctx context.Context, id string) (*controlplane.ConsoleSession, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var sess controlplane.ConsoleSession
	var roles, audit, createdAt, startedAt, endedAt string
	var tty int
	var exitCode sql.NullInt64
	err = db.QueryRowContext(ctx,
		`SELECT id, topology, node, agent_id, status, error, exit_code, requested_by, roles, policy, tty, audit, created_at, started_at, ended_at FROM sysbox_console_sessions WHERE id=?`, id).
		Scan(&sess.ID, &sess.Topology, &sess.Node, &sess.AgentID, &sess.Status, &sess.Err, &exitCode, &sess.RequestedBy, &roles, &sess.Policy, &tty, &audit, &createdAt, &startedAt, &endedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("console session not found")
	}
	if err != nil {
		return nil, err
	}
	sess.TTY = tty != 0
	if exitCode.Valid {
		v := int(exitCode.Int64)
		sess.ExitCode = &v
	}
	json.Unmarshal([]byte(roles), &sess.Roles)
	json.Unmarshal([]byte(audit), &sess.Audit)
	sess.CreatedAt = parseSQLiteTime(createdAt)
	sess.StartedAt = parseSQLiteTime(startedAt)
	sess.EndedAt = parseSQLiteTime(endedAt)
	return &sess, nil
}

func (s *sqliteAPIStore) ListConsoleSessions(ctx context.Context, workspace string) ([]controlplane.ConsoleSession, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, topology, node, agent_id, status, error, exit_code, requested_by, roles, policy, tty, audit, created_at, started_at, ended_at FROM sysbox_console_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.ConsoleSession
	for rows.Next() {
		var sess controlplane.ConsoleSession
		var roles, audit, createdAt, startedAt, endedAt string
		var tty int
		var exitCode sql.NullInt64
		if err := rows.Scan(&sess.ID, &sess.Topology, &sess.Node, &sess.AgentID, &sess.Status, &sess.Err, &exitCode, &sess.RequestedBy, &roles, &sess.Policy, &tty, &audit, &createdAt, &startedAt, &endedAt); err != nil {
			return nil, err
		}
		sess.TTY = tty != 0
		if exitCode.Valid {
			v := int(exitCode.Int64)
			sess.ExitCode = &v
		}
		json.Unmarshal([]byte(roles), &sess.Roles)
		json.Unmarshal([]byte(audit), &sess.Audit)
		sess.CreatedAt = parseSQLiteTime(createdAt)
		sess.StartedAt = parseSQLiteTime(startedAt)
		sess.EndedAt = parseSQLiteTime(endedAt)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ── Node operations ──────────────────────────────────────────────────────────

func (s *sqliteAPIStore) SaveNodeOperation(ctx context.Context, op controlplane.NodeOperation) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	roles, _ := json.Marshal(op.Roles)
	audit, _ := json.Marshal(op.Audit)
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_node_operations (id, topology, operation, node, agent_id, status, error, requested_by, roles, audit, created_at, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET status=excluded.status, error=excluded.error, ended_at=excluded.ended_at`,
		op.ID, op.Topology, op.Operation, op.Node, op.AgentID, op.Status, op.Err, op.RequestedBy, string(roles), string(audit),
		formatSQLiteTime(op.CreatedAt), formatSQLiteTime(op.StartedAt), formatSQLiteTime(op.EndedAt))
	return err
}

func (s *sqliteAPIStore) GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var op controlplane.NodeOperation
	var roles, audit, createdAt, startedAt, endedAt string
	err = db.QueryRowContext(ctx,
		`SELECT id, topology, operation, node, agent_id, status, error, requested_by, roles, audit, created_at, started_at, ended_at FROM sysbox_node_operations WHERE id=?`, id).
		Scan(&op.ID, &op.Topology, &op.Operation, &op.Node, &op.AgentID, &op.Status, &op.Err, &op.RequestedBy, &roles, &audit, &createdAt, &startedAt, &endedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("node operation not found")
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(roles), &op.Roles)
	json.Unmarshal([]byte(audit), &op.Audit)
	op.CreatedAt = parseSQLiteTime(createdAt)
	op.StartedAt = parseSQLiteTime(startedAt)
	op.EndedAt = parseSQLiteTime(endedAt)
	return &op, nil
}

func (s *sqliteAPIStore) ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, topology, operation, node, agent_id, status, error, requested_by, roles, audit, created_at, started_at, ended_at FROM sysbox_node_operations ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.NodeOperation
	for rows.Next() {
		var op controlplane.NodeOperation
		var roles, audit, createdAt, startedAt, endedAt string
		if err := rows.Scan(&op.ID, &op.Topology, &op.Operation, &op.Node, &op.AgentID, &op.Status, &op.Err, &op.RequestedBy, &roles, &audit, &createdAt, &startedAt, &endedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(roles), &op.Roles)
		json.Unmarshal([]byte(audit), &op.Audit)
		op.CreatedAt = parseSQLiteTime(createdAt)
		op.StartedAt = parseSQLiteTime(startedAt)
		op.EndedAt = parseSQLiteTime(endedAt)
		out = append(out, op)
	}
	return out, rows.Err()
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
	event, err = *mustDecodeEvent(raw), nil
	_ = err
	_, err = db.ExecContext(ctx, `INSERT INTO sysbox_agent_command_events (command_id, type, agent_id, status, message, error, created_at, record_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event.CommandID, event.Type, event.AgentID, event.Status, event.Message, event.Error, formatSQLiteTime(event.CreatedAt), raw)
	return err
}

func (s *sqliteAPIStore) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT command_id, type, agent_id, status, message, error, created_at, record_json FROM sysbox_agent_command_events WHERE agent_id=? OR agent_id=''`, agentID)
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
			ev = *decoded
		} else {
			ev.CreatedAt = parseSQLiteTime(createdAt)
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

func (s *sqliteAPIStore) SaveAgentInventory(ctx context.Context, inv controlplane.AgentInventory) error {
	raw, err := persistence.EncodeInventory(inv)
	if err != nil {
		return err
	}
	invPtr, err := persistence.DecodeInventory(raw)
	if err != nil {
		return err
	}
	inv = *invPtr
	db, err := s.open()
	if err != nil {
		return err
	}
	topologies, _ := json.Marshal(inv.Topologies)
	artifacts, _ := json.Marshal(inv.Artifacts)
	tools, _ := json.Marshal(inv.Tools)
	caps, _ := json.Marshal(inv.Capabilities)
	labels, _ := json.Marshal(inv.Labels)
	resources, _ := json.Marshal(inv.Resources)
	stale := 0
	if inv.Stale {
		stale = 1
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_agent_inventory (agent_id, capabilities, labels, resources, topologies, artifacts, tools, status, stale, observed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET capabilities=excluded.capabilities, labels=excluded.labels, resources=excluded.resources, topologies=excluded.topologies, artifacts=excluded.artifacts, tools=excluded.tools, status=excluded.status, stale=excluded.stale, observed_at=excluded.observed_at`,
		inv.AgentID, string(caps), string(labels), string(resources), string(topologies), string(artifacts), string(tools), inv.Status, stale, formatSQLiteTime(inv.ObservedAt))
	return err
}

func (s *sqliteAPIStore) GetAgentInventory(ctx context.Context, agentID string) (*controlplane.AgentInventory, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var inv controlplane.AgentInventory
	var caps, labels, resources, topologies, artifacts, tools []byte
	var observedAt string
	var stale int
	err = db.QueryRowContext(ctx,
		`SELECT agent_id, capabilities, labels, resources, topologies, artifacts, tools, status, stale, observed_at FROM sysbox_agent_inventory WHERE agent_id=?`, agentID).
		Scan(&inv.AgentID, &caps, &labels, &resources, &topologies, &artifacts, &tools, &inv.Status, &stale, &observedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent inventory not found")
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(caps, &inv.Capabilities)
	json.Unmarshal(labels, &inv.Labels)
	json.Unmarshal(resources, &inv.Resources)
	json.Unmarshal(topologies, &inv.Topologies)
	json.Unmarshal(artifacts, &inv.Artifacts)
	json.Unmarshal(tools, &inv.Tools)
	inv.Stale = stale != 0
	inv.ObservedAt = parseSQLiteTime(observedAt)
	if inv.AgentID == "" {
		return nil, persistence.ErrCorruptRecord
	}
	return &inv, nil
}
