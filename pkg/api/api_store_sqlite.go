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
	db, err := sql.Open("sqlite", s.dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
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
		plan_id     TEXT DEFAULT '',
		agent_id    TEXT DEFAULT '',
		recoverable INTEGER DEFAULT 0,
		protocol    TEXT DEFAULT '',
		lease_owner TEXT DEFAULT '',
		lease_until TEXT DEFAULT '',
		attempt     INTEGER NOT NULL DEFAULT 0,
		queued_at   TEXT DEFAULT '',
		assigned_at TEXT DEFAULT '',
		started_at  TEXT DEFAULT '',
		ended_at    TEXT DEFAULT ''
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
		topologies   TEXT DEFAULT '[]',
		artifacts    TEXT DEFAULT '[]',
		tools        TEXT DEFAULT '[]',
		status       TEXT DEFAULT '',
		stale        INTEGER DEFAULT 0,
		observed_at  TEXT NOT NULL DEFAULT ''
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

	CREATE TABLE IF NOT EXISTS sysbox_revisions (
		id         TEXT NOT NULL DEFAULT '',
		workspace  TEXT NOT NULL DEFAULT '',
		source     TEXT DEFAULT '',
		sha256     TEXT DEFAULT '',
		size       INTEGER DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT '',
		description TEXT DEFAULT '',
		PRIMARY KEY (id, workspace)
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
	return t.Format(time.RFC3339)
}

// ── apiStore interface ───────────────────────────────────────────────────────

func (s *sqliteAPIStore) SchemaVersion(context.Context) (int, error) {
	return apiSchemaVersion, nil
}

func (s *sqliteAPIStore) LoadRuns(ctx context.Context) ([]Run, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, topology, operation, op, status, error, parent_id, revision, plan_id, agent_id, recoverable, protocol, lease_owner, lease_until, attempt, queued_at, assigned_at, started_at, ended_at FROM sysbox_runs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var leaseUntil, queuedAt, assignedAt, startedAt, endedAt string
		var recoverable int
		if err := rows.Scan(&r.ID, &r.Topology, &r.Operation, &r.Op, &r.Status, &r.Err, &r.ParentID, &r.Revision, &r.PlanID, &r.AgentID, &recoverable, &r.Protocol, &r.LeaseOwner, &leaseUntil, &r.Attempt, &queuedAt, &assignedAt, &startedAt, &endedAt); err != nil {
			return nil, err
		}
		r.Recoverable = recoverable != 0
		r.LeaseUntil = parseSQLiteTime(leaseUntil)
		r.QueuedAt = parseSQLiteTime(queuedAt)
		r.AssignedAt = parseSQLiteTime(assignedAt)
		r.StartedAt = parseSQLiteTime(startedAt)
		r.EndedAt = parseSQLiteTime(endedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) GetRun(ctx context.Context, id string) (*Run, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx, `SELECT id, topology, operation, op, status, error, parent_id, revision, plan_id, agent_id, recoverable, protocol, lease_owner, lease_until, attempt, queued_at, assigned_at, started_at, ended_at FROM sysbox_runs WHERE id=?`, id)
	var r Run
	var leaseUntil, queuedAt, assignedAt, startedAt, endedAt string
	var recoverable int
	if err := row.Scan(&r.ID, &r.Topology, &r.Operation, &r.Op, &r.Status, &r.Err, &r.ParentID, &r.Revision, &r.PlanID, &r.AgentID, &recoverable, &r.Protocol, &r.LeaseOwner, &leaseUntil, &r.Attempt, &queuedAt, &assignedAt, &startedAt, &endedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("run not found")
		}
		return nil, err
	}
	r.Recoverable = recoverable != 0
	r.LeaseUntil = parseSQLiteTime(leaseUntil)
	r.QueuedAt = parseSQLiteTime(queuedAt)
	r.AssignedAt = parseSQLiteTime(assignedAt)
	r.StartedAt = parseSQLiteTime(startedAt)
	r.EndedAt = parseSQLiteTime(endedAt)
	normalizeRunProductFields(&r)
	return &r, nil
}

func (s *sqliteAPIStore) SaveRun(ctx context.Context, run Run) error {
	normalizeRunProductFields(&run)
	db, err := s.open()
	if err != nil {
		return err
	}
	recoverable := 0
	if run.Recoverable {
		recoverable = 1
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_runs (id, topology, operation, op, status, error, parent_id, revision, plan_id, agent_id, recoverable, protocol, lease_owner, lease_until, attempt, queued_at, assigned_at, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Topology, run.Operation, run.Op, run.Status, run.Err, run.ParentID, run.Revision, run.PlanID, run.AgentID, recoverable, run.Protocol, run.LeaseOwner, formatSQLiteTime(run.LeaseUntil), run.Attempt, formatSQLiteTime(run.QueuedAt), formatSQLiteTime(run.AssignedAt), formatSQLiteTime(run.StartedAt), formatSQLiteTime(run.EndedAt))
	return err
}

func (s *sqliteAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error) {
	db, err := s.open()
	if err != nil {
		return nil, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var status, leaseOwner, leaseUntilStr, colAgentID string
	var recoverable int
	err = tx.QueryRowContext(ctx, `SELECT status, agent_id, lease_owner, lease_until, recoverable FROM sysbox_runs WHERE id=?`, runID).
		Scan(&status, &colAgentID, &leaseOwner, &leaseUntilStr, &recoverable)
	if err == sql.ErrNoRows {
		return nil, false, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, false, err
	}
	lu := parseSQLiteTime(leaseUntilStr)
	if !runLeasable(Run{Status: controlplane.RunStatus(status), AgentID: colAgentID, LeaseOwner: leaseOwner, LeaseUntil: lu, Recoverable: recoverable != 0, ID: runID}, agentID, time.Now().UTC()) {
		return &Run{ID: runID, Status: controlplane.RunStatus(status), AgentID: colAgentID, LeaseOwner: leaseOwner, LeaseUntil: lu}, false, nil
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE sysbox_runs SET status='running', lease_owner=?, lease_until=?, attempt=attempt+1, started_at=CASE WHEN started_at='' THEN ? ELSE started_at END WHERE id=?`,
		owner, formatSQLiteTime(now.Add(ttl)), formatSQLiteTime(now), runID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	r, err := s.GetRun(ctx, runID)
	if err != nil {
		return r, false, err
	}
	return r, true, nil
}

func (s *sqliteAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*Run, bool, error) {
	db, err := s.open()
	if err != nil {
		return nil, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var status, leaseOwner, colAgentID string
	err = tx.QueryRowContext(ctx, `SELECT status, agent_id, lease_owner FROM sysbox_runs WHERE id=?`, runID).
		Scan(&status, &colAgentID, &leaseOwner)
	if err == sql.ErrNoRows {
		return nil, false, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, false, err
	}
	if colAgentID != agentID || status != "running" || leaseOwner != owner {
		return &Run{ID: runID, Status: controlplane.RunStatus(status), LeaseOwner: leaseOwner, AgentID: colAgentID}, false, nil
	}
	newLU := formatSQLiteTime(time.Now().UTC().Add(ttl))
	if _, err := tx.ExecContext(ctx, `UPDATE sysbox_runs SET lease_until=? WHERE id=?`, newLU, runID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	r, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	return r, true, nil
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

func (s *sqliteAPIStore) SaveRevision(ctx context.Context, rev controlplane.Revision) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_revisions (id, workspace, source, sha256, size, created_at, description) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rev.ID, rev.Workspace, rev.Source, rev.SHA256, rev.Size, rev.CreatedAt.Format(time.RFC3339), rev.Description)
	return err
}

func (s *sqliteAPIStore) ListRevisions(ctx context.Context, workspace string) ([]controlplane.Revision, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, workspace, source, sha256, size, created_at, description FROM sysbox_revisions WHERE workspace=? ORDER BY created_at DESC`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.Revision
	for rows.Next() {
		var rev controlplane.Revision
		var createdAt string
		if err := rows.Scan(&rev.ID, &rev.Workspace, &rev.Source, &rev.SHA256, &rev.Size, &createdAt, &rev.Description); err != nil {
			return nil, err
		}
		rev.CreatedAt = parseSQLiteTime(createdAt)
		out = append(out, rev)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var rev controlplane.Revision
	var createdAt string
	err = db.QueryRowContext(ctx,
		`SELECT id, workspace, source, sha256, size, created_at, description FROM sysbox_revisions WHERE workspace=? AND id=?`,
		workspace, revisionID).Scan(&rev.ID, &rev.Workspace, &rev.Source, &rev.SHA256, &rev.Size, &createdAt, &rev.Description)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("revision not found")
	}
	if err != nil {
		return nil, err
	}
	rev.CreatedAt = parseSQLiteTime(createdAt)
	return &rev, nil
}

// ── Plans / Policies ─────────────────────────────────────────────────────────

func (s *sqliteAPIStore) SavePlan(ctx context.Context, plan controlplane.Plan) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	actions, _ := json.Marshal(plan.Actions)
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_plans (id, workspace, revision, state_serial, status, summary, actions, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.Workspace, plan.Revision, plan.StateSerial, plan.Status, plan.Summary, actions, plan.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *sqliteAPIStore) ListPlans(ctx context.Context, workspace string) ([]controlplane.Plan, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, workspace, revision, state_serial, status, summary, actions, created_at FROM sysbox_plans WHERE workspace=? ORDER BY created_at DESC`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.Plan
	for rows.Next() {
		var p controlplane.Plan
		var actions []byte
		var createdAt string
		if err := rows.Scan(&p.ID, &p.Workspace, &p.Revision, &p.StateSerial, &p.Status, &p.Summary, &actions, &createdAt); err != nil {
			return nil, err
		}
		json.Unmarshal(actions, &p.Actions)
		p.CreatedAt = parseSQLiteTime(createdAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var p controlplane.Plan
	var actions []byte
	var createdAt string
	err = db.QueryRowContext(ctx,
		`SELECT id, workspace, revision, state_serial, status, summary, actions, created_at FROM sysbox_plans WHERE workspace=? AND id=?`, workspace, planID).
		Scan(&p.ID, &p.Workspace, &p.Revision, &p.StateSerial, &p.Status, &p.Summary, &actions, &createdAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("plan not found")
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(actions, &p.Actions)
	p.CreatedAt = parseSQLiteTime(createdAt)
	return &p, nil
}

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

func (s *sqliteAPIStore) SaveAgent(ctx context.Context, agent controlplane.Agent) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	caps, _ := json.Marshal(agent.Capabilities)
	labels, _ := json.Marshal(agent.Labels)
	disabled, quarantined := 0, 0
	if agent.Disabled {
		disabled = 1
	}
	if agent.Quarantined {
		quarantined = 1
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_agents (id, name, status, disabled, quarantined, reason, auth_secret, secret_hash, protocol, capabilities, labels, version, last_heartbeat, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET status=excluded.status, disabled=excluded.disabled, quarantined=excluded.quarantined, reason=excluded.reason, protocol=excluded.protocol, capabilities=excluded.capabilities, labels=excluded.labels, version=excluded.version, last_heartbeat=excluded.last_heartbeat, updated_at=excluded.updated_at`,
		agent.ID, agent.Name, agent.Status, disabled, quarantined, agent.Reason, agent.AuthSecret, agent.SecretHash, agent.Protocol, string(caps), string(labels), agent.Version,
		formatSQLiteTime(agent.LastHeartbeat), formatSQLiteTime(agent.CreatedAt), formatSQLiteTime(agent.UpdatedAt))
	return err
}

func (s *sqliteAPIStore) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var agent controlplane.Agent
	var caps, labels []byte
	var lastHB, createdAt, updatedAt string
	var disabled, quarantined int
	err = db.QueryRowContext(ctx,
		`SELECT id, name, status, disabled, quarantined, reason, auth_secret, secret_hash, protocol, capabilities, labels, version, last_heartbeat, created_at, updated_at FROM sysbox_agents WHERE id=?`, id).
		Scan(&agent.ID, &agent.Name, &agent.Status, &disabled, &quarantined, &agent.Reason, &agent.AuthSecret, &agent.SecretHash, &agent.Protocol, &caps, &labels, &agent.Version, &lastHB, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent not found")
	}
	if err != nil {
		return nil, err
	}
	agent.Disabled = disabled != 0
	agent.Quarantined = quarantined != 0
	json.Unmarshal(caps, &agent.Capabilities)
	json.Unmarshal(labels, &agent.Labels)
	agent.LastHeartbeat = parseSQLiteTime(lastHB)
	agent.CreatedAt = parseSQLiteTime(createdAt)
	agent.UpdatedAt = parseSQLiteTime(updatedAt)
	return &agent, nil
}

func (s *sqliteAPIStore) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, status, disabled, quarantined, reason, auth_secret, secret_hash, protocol, capabilities, labels, version, last_heartbeat, created_at, updated_at FROM sysbox_agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.Agent
	for rows.Next() {
		var agent controlplane.Agent
		var caps, labels []byte
		var lastHB, createdAt, updatedAt string
		var disabled, quarantined int
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Status, &disabled, &quarantined, &agent.Reason, &agent.AuthSecret, &agent.SecretHash, &agent.Protocol, &caps, &labels, &agent.Version, &lastHB, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		agent.Disabled = disabled != 0
		agent.Quarantined = quarantined != 0
		json.Unmarshal(caps, &agent.Capabilities)
		json.Unmarshal(labels, &agent.Labels)
		agent.LastHeartbeat = parseSQLiteTime(lastHB)
		agent.CreatedAt = parseSQLiteTime(createdAt)
		agent.UpdatedAt = parseSQLiteTime(updatedAt)
		out = append(out, agent)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_agent_command_events (command_id, type, agent_id, status, message, error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.CommandID, event.Type, event.AgentID, event.Status, event.Message, event.Error, formatSQLiteTime(event.CreatedAt))
	return err
}

func (s *sqliteAPIStore) ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT command_id, type, agent_id, status, message, error, created_at FROM sysbox_agent_command_events WHERE agent_id=? OR agent_id=''`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.AgentCommandEvent
	for rows.Next() {
		var ev controlplane.AgentCommandEvent
		var createdAt string
		if err := rows.Scan(&ev.CommandID, &ev.Type, &ev.AgentID, &ev.Status, &ev.Message, &ev.Error, &createdAt); err != nil {
			return nil, err
		}
		ev.CreatedAt = parseSQLiteTime(createdAt)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	runPayload, _ := json.Marshal(cmd.Run)
	sessionPayload, _ := json.Marshal(cmd.Session)
	operationPayload, _ := json.Marshal(cmd.Operation)
	requestPayload, _ := json.Marshal(cmd.Request)
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_agent_commands (id, agent_id, type, status, error, protocol, run_payload, session_payload, operation_payload, request_payload, lease_owner, lease_until, attempt, created_at, delivered, acked_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET status=excluded.status, error=excluded.error, lease_owner=excluded.lease_owner, lease_until=excluded.lease_until, attempt=excluded.attempt, acked_at=excluded.acked_at, ended_at=excluded.ended_at`,
		cmd.ID, cmd.AgentID, cmd.Type, cmd.Status, cmd.Err, cmd.Protocol, runPayload, sessionPayload, operationPayload, requestPayload,
		cmd.LeaseOwner, formatSQLiteTime(cmd.LeaseUntil), cmd.Attempt,
		formatSQLiteTime(cmd.CreatedAt), formatSQLiteTime(cmd.Delivered), formatSQLiteTime(cmd.AckedAt), formatSQLiteTime(cmd.EndedAt))
	return err
}

func (s *sqliteAPIStore) ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, agent_id, type, status, error, protocol, run_payload, session_payload, operation_payload, request_payload, lease_owner, lease_until, attempt, created_at, delivered, acked_at, ended_at FROM sysbox_agent_commands WHERE agent_id=? OR agent_id='' ORDER BY created_at DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.AgentCommand
	for rows.Next() {
		var cmd controlplane.AgentCommand
		var runPayload, sessionPayload, operationPayload, reqPayload []byte
		var leaseUntil, createdAt, delivered, ackedAt, endedAt string
		if err := rows.Scan(&cmd.ID, &cmd.AgentID, &cmd.Type, &cmd.Status, &cmd.Err, &cmd.Protocol, &runPayload, &sessionPayload, &operationPayload, &reqPayload, &cmd.LeaseOwner, &leaseUntil, &cmd.Attempt, &createdAt, &delivered, &ackedAt, &endedAt); err != nil {
			return nil, err
		}
		if runPayload != nil {
			json.Unmarshal(runPayload, &cmd.Run)
		}
		if sessionPayload != nil {
			json.Unmarshal(sessionPayload, &cmd.Session)
		}
		if operationPayload != nil {
			json.Unmarshal(operationPayload, &cmd.Operation)
		}
		if reqPayload != nil {
			json.Unmarshal(reqPayload, &cmd.Request)
		}
		cmd.LeaseUntil = parseSQLiteTime(leaseUntil)
		cmd.CreatedAt = parseSQLiteTime(createdAt)
		cmd.Delivered = parseSQLiteTime(delivered)
		cmd.AckedAt = parseSQLiteTime(ackedAt)
		cmd.EndedAt = parseSQLiteTime(endedAt)
		out = append(out, cmd)
	}
	return out, rows.Err()
}

func (s *sqliteAPIStore) AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	db, err := s.open()
	if err != nil {
		return nil, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var status, leaseOwner, leaseUntilStr string
	err = tx.QueryRowContext(ctx,
		`SELECT status, lease_owner, lease_until FROM sysbox_agent_commands WHERE id=? AND agent_id=?`, commandID, agentID).
		Scan(&status, &leaseOwner, &leaseUntilStr)
	if err == sql.ErrNoRows {
		return nil, false, fmt.Errorf("agent command not found")
	}
	if err != nil {
		return nil, false, err
	}
	lu := parseSQLiteTime(leaseUntilStr)
	now := time.Now().UTC()
	if status == "done" || status == "failed" || status == "cancelled" {
		return &controlplane.AgentCommand{ID: commandID, AgentID: agentID, Status: status}, false, nil
	}
	if leaseOwner != "" && lu.After(now) && leaseOwner != owner {
		return &controlplane.AgentCommand{ID: commandID, AgentID: agentID, Status: status, LeaseOwner: leaseOwner, LeaseUntil: lu}, false, nil
	}
	newLU := formatSQLiteTime(now.Add(ttl))
	if _, err := tx.ExecContext(ctx,
		`UPDATE sysbox_agent_commands SET status='leased', lease_owner=?, lease_until=?, attempt=attempt+1 WHERE id=?`,
		owner, newLU, commandID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	cmd, err := s.loadAgentCommand(ctx, commandID)
	if err != nil {
		return nil, false, err
	}
	return cmd, true, nil
}

func (s *sqliteAPIStore) loadAgentCommand(ctx context.Context, id string) (*controlplane.AgentCommand, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var cmd controlplane.AgentCommand
	var runPayload, sessionPayload, operationPayload, reqPayload []byte
	var leaseUntil, createdAt, delivered, ackedAt, endedAt string
	err = db.QueryRowContext(ctx,
		`SELECT id, agent_id, type, status, error, protocol, run_payload, session_payload, operation_payload, request_payload, lease_owner, lease_until, attempt, created_at, delivered, acked_at, ended_at FROM sysbox_agent_commands WHERE id=?`, id).
		Scan(&cmd.ID, &cmd.AgentID, &cmd.Type, &cmd.Status, &cmd.Err, &cmd.Protocol, &runPayload, &sessionPayload, &operationPayload, &reqPayload, &cmd.LeaseOwner, &leaseUntil, &cmd.Attempt, &createdAt, &delivered, &ackedAt, &endedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("command not found after update")
	}
	if err != nil {
		return nil, err
	}
	if runPayload != nil {
		json.Unmarshal(runPayload, &cmd.Run)
	}
	if sessionPayload != nil {
		json.Unmarshal(sessionPayload, &cmd.Session)
	}
	if operationPayload != nil {
		json.Unmarshal(operationPayload, &cmd.Operation)
	}
	if reqPayload != nil {
		json.Unmarshal(reqPayload, &cmd.Request)
	}
	cmd.LeaseUntil = parseSQLiteTime(leaseUntil)
	cmd.CreatedAt = parseSQLiteTime(createdAt)
	cmd.Delivered = parseSQLiteTime(delivered)
	cmd.AckedAt = parseSQLiteTime(ackedAt)
	cmd.EndedAt = parseSQLiteTime(endedAt)
	return &cmd, nil
}

func (s *sqliteAPIStore) SaveAgentInventory(ctx context.Context, inv controlplane.AgentInventory) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	topologies, _ := json.Marshal(inv.Topologies)
	artifacts, _ := json.Marshal(inv.Artifacts)
	tools, _ := json.Marshal(inv.Tools)
	caps, _ := json.Marshal(inv.Capabilities)
	labels, _ := json.Marshal(inv.Labels)
	stale := 0
	if inv.Stale {
		stale = 1
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_agent_inventory (agent_id, capabilities, labels, topologies, artifacts, tools, status, stale, observed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET capabilities=excluded.capabilities, labels=excluded.labels, topologies=excluded.topologies, artifacts=excluded.artifacts, tools=excluded.tools, status=excluded.status, stale=excluded.stale, observed_at=excluded.observed_at`,
		inv.AgentID, string(caps), string(labels), string(topologies), string(artifacts), string(tools), inv.Status, stale, formatSQLiteTime(inv.ObservedAt))
	return err
}

func (s *sqliteAPIStore) GetAgentInventory(ctx context.Context, agentID string) (*controlplane.AgentInventory, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var inv controlplane.AgentInventory
	var caps, labels, topologies, artifacts, tools []byte
	var observedAt string
	var stale int
	err = db.QueryRowContext(ctx,
		`SELECT agent_id, capabilities, labels, topologies, artifacts, tools, status, stale, observed_at FROM sysbox_agent_inventory WHERE agent_id=?`, agentID).
		Scan(&inv.AgentID, &caps, &labels, &topologies, &artifacts, &tools, &inv.Status, &stale, &observedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent inventory not found")
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(caps, &inv.Capabilities)
	json.Unmarshal(labels, &inv.Labels)
	json.Unmarshal(topologies, &inv.Topologies)
	json.Unmarshal(artifacts, &inv.Artifacts)
	json.Unmarshal(tools, &inv.Tools)
	inv.Stale = stale != 0
	inv.ObservedAt = parseSQLiteTime(observedAt)
	return &inv, nil
}
