package controlstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type LocalRunStorage struct{ Store *Local }

func (s *Local) RestartRun(ctx context.Context, run controlplane.Run) (*controlplane.Run, error) {
	return persistence.NewRunRepository(LocalRunStorage{Store: s}).RestartRun(ctx, run)
}

func (s *SQLite) RestartRun(ctx context.Context, run controlplane.Run) (*controlplane.Run, error) {
	return persistence.NewRunRepository(SqliteRunStorage{Store: s}).RestartRun(ctx, run)
}

func (s *Postgres) RestartRun(ctx context.Context, run controlplane.Run) (*controlplane.Run, error) {
	return persistence.NewRunRepository(PostgresRunStorage{Store: s}).RestartRun(ctx, run)
}

func (s *Local) ExpireRunLease(ctx context.Context, id string, now time.Time) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(LocalRunStorage{Store: s}).ExpireRunLease(ctx, id, now)
}

func (s *SQLite) ExpireRunLease(ctx context.Context, id string, now time.Time) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(SqliteRunStorage{Store: s}).ExpireRunLease(ctx, id, now)
}

func (s *Postgres) ExpireRunLease(ctx context.Context, id string, now time.Time) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(PostgresRunStorage{Store: s}).ExpireRunLease(ctx, id, now)
}

type SqliteRunStorage struct{ Store *SQLite }

type PostgresRunStorage struct{ Store *Postgres }

func (s *Local) LockRunRecords(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.RunsDir == "" {
		return nil, persistence.ErrInvalidRecord
	}
	dir := filepath.Join(s.RunsDir, ".run-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "records.lock"))
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if !locked {
		lock.Close()
		return nil, ctx.Err()
	}
	return func() { _ = lock.Close() }, nil
}

func (b LocalRunStorage) UpdateRun(ctx context.Context, id string, transform func(*controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error) {
	return b.updateRunWithCommand(ctx, id, "", func(run *controlplane.Run, _ *persistence.CommandRecord) (*controlplane.Run, error) {
		return transform(run)
	})
}

func (b LocalRunStorage) updateRunWithCommand(ctx context.Context, id, commandID string, transform func(*controlplane.Run, *persistence.CommandRecord) (*controlplane.Run, error)) (*controlplane.Run, error) {
	unlock, err := b.Store.LockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	current, err := b.Store.GetRun(ctx, id)
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return nil, err
	}
	var command *persistence.CommandRecord
	if commandID != "" {
		command, err = (LocalCommandStorage{Store: b.Store}).Read(ctx, commandID)
		if err != nil {
			return nil, err
		}
	}
	next, err := transform(current, command)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return current, nil
	}
	runs, err := b.Store.LoadRuns(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateTopologyAdmission(runs, *next); err != nil {
		return nil, err
	}
	err = persistence.FilesystemExecutionGrantStorage(b.Store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
		if err := checkLocalRunMaintenance(ctx, b.Store.RunsDir, authority, *next); err != nil {
			return nil, err
		}
		if persistence.LiveRunLeaseRequired(ctx) && (current == nil || !current.LeaseUntil.After(time.Now()) || !current.DeadlineAt.IsZero() && !current.DeadlineAt.After(time.Now())) {
			return nil, persistence.ErrConflict
		}
		return nil, b.Store.SaveRunUnlocked(ctx, *next)
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

const SqliteRunColumns = `id, topology, operation, op, status, error, parent_id, revision, target, agent_id, recoverable, unsafe_state, operation_key, request_fingerprint, protocol, lease_owner, lease_until, attempt, queued_at, assigned_at, started_at, ended_at, deadline_at, snapshot_path, record_json`

type runScanner interface{ Scan(...any) error }

func ScanSQLiteRun(row runScanner) (*controlplane.Run, error) {
	var r controlplane.Run
	var leaseUntil, queuedAt, assignedAt, startedAt, endedAt, deadlineAt string
	var recoverable, unsafeState int
	var full []byte
	if err := row.Scan(&r.ID, &r.Topology, &r.Operation, &r.Op, &r.Status, &r.Err, &r.ParentID, &r.Revision, &r.Target, &r.AgentID, &recoverable, &unsafeState, &r.OperationKey, &r.RequestFingerprint, &r.Protocol, &r.LeaseOwner, &leaseUntil, &r.Attempt, &queuedAt, &assignedAt, &startedAt, &endedAt, &deadlineAt, &r.SnapshotPath, &full); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	r.Recoverable = recoverable != 0
	r.UnsafeState = unsafeState != 0
	for _, field := range []struct {
		raw string
		dst *time.Time
	}{{leaseUntil, &r.LeaseUntil}, {queuedAt, &r.QueuedAt}, {assignedAt, &r.AssignedAt}, {startedAt, &r.StartedAt}, {endedAt, &r.EndedAt}, {deadlineAt, &r.DeadlineAt}} {
		if field.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, field.raw)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid run timestamp", persistence.ErrCorruptRecord)
		}
		*field.dst = parsed
	}
	controlplane.NormalizeRun(&r)
	if len(full) > 0 {
		var snapshot *controlplane.Run
		if json.Unmarshal(full, &snapshot) != nil || snapshot == nil {
			return nil, fmt.Errorf("%w: invalid full run snapshot", persistence.ErrCorruptRecord)
		}
		if len(snapshot.Inputs) > 0 || !sameSQLiteRunColumns(r, *snapshot) {
			return nil, fmt.Errorf("%w: inconsistent run snapshot", persistence.ErrCorruptRecord)
		}
		controlplane.NormalizeRun(snapshot)
		return snapshot, nil
	}
	return nil, fmt.Errorf("%w: run snapshot missing; rebuild the database", persistence.ErrCorruptRecord)
}

func sameSQLiteRunColumns(indexed, full controlplane.Run) bool {
	comparable := func(run controlplane.Run) controlplane.Run {
		// These fields exist only in the complete snapshot, not the legacy
		// index columns. Every indexed field, including QueuedAt, must agree.
		run.ProjectID = ""
		run.Workspace = ""
		run.Assertion = nil
		run.Cleanup = nil
		run.HostCompletion = nil
		run.Inputs = nil
		run.QueuedAt = run.QueuedAt.UTC()
		run.AssignedAt = run.AssignedAt.UTC()
		run.StartedAt = run.StartedAt.UTC()
		run.EndedAt = run.EndedAt.UTC()
		run.LeaseUntil = run.LeaseUntil.UTC()
		run.DeadlineAt = run.DeadlineAt.UTC()
		return run
	}
	a, errA := json.Marshal(comparable(indexed))
	b, errB := json.Marshal(comparable(full))
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func (b SqliteRunStorage) UpdateRun(ctx context.Context, id string, transform func(*controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error) {
	return b.updateRunWithCommand(ctx, id, "", func(run *controlplane.Run, _ *persistence.CommandRecord) (*controlplane.Run, error) {
		return transform(run)
	})
}

func (b SqliteRunStorage) updateRunWithCommand(ctx context.Context, id, commandID string, transform func(*controlplane.Run, *persistence.CommandRecord) (*controlplane.Run, error)) (*controlplane.Run, error) {
	db, err := b.Store.OpenDB()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	current, err := ScanSQLiteRun(conn.QueryRowContext(ctx, "SELECT "+SqliteRunColumns+" FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1", id))
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return nil, err
	}
	var command *persistence.CommandRecord
	if commandID != "" {
		command, err = ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", commandID))
		if err != nil {
			return nil, err
		}
	}
	next, err := transform(current, command)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if err := checkSQLiteRunMaintenance(ctx, conn, *next); err != nil {
			return nil, err
		}
		if err := checkSQLiteRunAdmission(ctx, conn, *next); err != nil {
			return nil, err
		}
		err = InsertSQLiteRun(ctx, conn, *next)
		if err != nil {
			return nil, err
		}
		current = next
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return current, nil
}

func (b PostgresRunStorage) UpdateRun(ctx context.Context, id string, transform func(*controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error) {
	return b.updateRunWithCommand(ctx, id, "", func(run *controlplane.Run, _ *persistence.CommandRecord) (*controlplane.Run, error) {
		return transform(run)
	})
}

func (b PostgresRunStorage) updateRunWithCommand(ctx context.Context, id, commandID string, transform func(*controlplane.Run, *persistence.CommandRecord) (*controlplane.Run, error)) (*controlplane.Run, error) {
	pool, err := b.Store.PoolFor(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var raw []byte
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))`); err != nil {
		return nil, err
	}
	// Use the same order as recovery/dispatch: authority before Run or command.
	initial := &GrantTransaction{}
	if err := persistence.NewExecutionGrantRepository(initial).ObserveTime(ctx); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority',$1::jsonb) ON CONFLICT(id) DO NOTHING", string(initial.Raw)); err != nil {
		return nil, err
	}
	var authority []byte
	if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1 FOR UPDATE`, id).Scan(&raw)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return nil, err
	}
	var current *controlplane.Run
	if !missing {
		if json.Unmarshal(raw, &current) != nil || current == nil {
			return nil, fmt.Errorf("%w: invalid run record", persistence.ErrCorruptRecord)
		}
		controlplane.NormalizeRun(current)
	}
	var command *persistence.CommandRecord
	if commandID != "" {
		command, err = ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", commandID))
		if err != nil {
			return nil, err
		}
	}
	next, err := transform(current, command)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if err := checkPostgresRunMaintenance(ctx, tx, authority, *next); err != nil {
			return nil, err
		}
		if err := checkPostgresRunAdmission(ctx, tx, *next); err != nil {
			return nil, err
		}
		durable := persistence.DurableRun(*next)
		raw, err = json.Marshal(durable)
		if err != nil {
			return nil, err
		}
		if missing {
			tag, insertErr := tx.Exec(ctx, `INSERT INTO sysbox_runs(topology,id,status,agent_id,lease_owner,lease_until,attempt,data,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,now()) ON CONFLICT(id) DO NOTHING`, durable.Topology, id, durable.Status, durable.AgentID, durable.LeaseOwner, NullableTime(durable.LeaseUntil), durable.Attempt, string(raw))
			err = insertErr
			if err == nil && tag.RowsAffected() != 1 {
				err = persistence.ErrConflict
			}
		} else {
			tag, updateErr := tx.Exec(ctx, `UPDATE sysbox_runs SET topology=$2,status=$3,agent_id=$4,lease_owner=$5,lease_until=$6,attempt=$7,data=$8::jsonb,updated_at=now() WHERE id=$1 AND (NOT $9 OR (status='running' AND lease_until>clock_timestamp() AND ($10::timestamptz IS NULL OR $10::timestamptz>clock_timestamp())))`, id, durable.Topology, durable.Status, durable.AgentID, durable.LeaseOwner, NullableTime(durable.LeaseUntil), durable.Attempt, string(raw), persistence.LiveRunLeaseRequired(ctx), NullableTime(current.DeadlineAt))
			err = updateErr
			if err == nil && tag.RowsAffected() != 1 {
				err = persistence.ErrConflict
			}
		}
		if err != nil {
			return nil, err
		}
		current = &durable
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return current, nil
}
