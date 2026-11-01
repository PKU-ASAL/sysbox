package controlstore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Transactions) ReleaseHostFence(ctx context.Context, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	var result persistence.HostFenceRelease
	topology, uid := fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID
	switch store := s.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return result, err
		}
		defer unlock()
		repo, err := s.ActionRepository(topology)
		if err != nil {
			return result, err
		}
		return repo.CommitFilesystemHostFenceRelease(ctx, store.RunsDir, topology, request, s.GrantHostKeys[fence.Request.Grant.AgentID], func() (controlplane.Run, persistence.CommandRecord, error) {
			if err := CheckLocalDeleteActivity(ctx, store, topology, uid); err != nil {
				return controlplane.Run{}, persistence.CommandRecord{}, err
			}
			run, err := store.GetRun(ctx, fence.Request.RunID)
			if err != nil {
				return controlplane.Run{}, persistence.CommandRecord{}, err
			}
			command, err := (LocalCommandStorage{Store: store}).Read(ctx, request.ObservationCommandID)
			if err != nil {
				return controlplane.Run{}, persistence.CommandRecord{}, err
			}
			if command == nil {
				return controlplane.Run{}, persistence.CommandRecord{}, persistence.ErrNotFound
			}
			return *run, *command, nil
		})
	case *SQLite:
		return s.releaseSQLiteHostFence(ctx, store, fence, request)
	case *Postgres:
		return s.releasePostgresHostFence(ctx, store, fence, request)
	default:
		return result, persistence.ErrUnsupportedVersion
	}
}

func (s *Transactions) releaseSQLiteHostFence(ctx context.Context, store *SQLite, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	var zero persistence.HostFenceRelease
	db, err := store.OpenDB()
	if err != nil {
		return zero, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return zero, err
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
	var raw, lifecycle []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw); err != nil {
		return zero, err
	}
	prior, err := persistence.ReplayHostFenceRelease(raw, request)
	if err != nil {
		return zero, err
	}
	if prior != nil {
		return *prior, nil
	}
	if err = CheckSQLiteDeleteActivity(ctx, conn, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID); err != nil {
		return zero, err
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", fence.Request.Topology).Scan(&lifecycle); err != nil {
		return zero, err
	}
	run, err := ScanSQLiteRun(conn.QueryRowContext(ctx, "SELECT "+SqliteRunColumns+" FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1", fence.Request.RunID))
	if err != nil {
		return zero, err
	}
	command, err := ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", request.ObservationCommandID))
	if err != nil {
		return zero, err
	}
	if run == nil || command == nil {
		return zero, persistence.ErrNotFound
	}
	next, result, err := persistence.ApplyHostFenceRelease(raw, lifecycle, *run, *command, s.GrantHostKeys[fence.Request.Grant.AgentID], request, time.Now().UTC())
	if err != nil {
		return zero, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", next); err != nil {
		return zero, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return zero, err
	}
	committed = true
	return result, nil
}

func (s *Transactions) releasePostgresHostFence(ctx context.Context, store *Postgres, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	var zero persistence.HostFenceRelease
	pool, err := store.PoolFor(ctx)
	if err != nil {
		return zero, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return zero, err
	}
	var raw, lifecycle, runRaw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&raw); err != nil {
		return zero, err
	}
	prior, err := persistence.ReplayHostFenceRelease(raw, request)
	if err != nil {
		return zero, err
	}
	if prior != nil {
		return *prior, nil
	}
	if err = CheckPostgresDeleteActivity(ctx, tx, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID); err != nil {
		return zero, err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", fence.Request.Topology).Scan(&lifecycle); err != nil {
		return zero, err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_runs WHERE id=$1 FOR UPDATE", fence.Request.RunID).Scan(&runRaw); err != nil {
		return zero, err
	}
	var run controlplane.Run
	if json.Unmarshal(runRaw, &run) != nil {
		return zero, persistence.ErrCorruptRecord
	}
	command, err := ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", request.ObservationCommandID))
	if err != nil {
		return zero, err
	}
	if command == nil {
		return zero, persistence.ErrNotFound
	}
	next, result, err := persistence.ApplyHostFenceRelease(raw, lifecycle, run, *command, s.GrantHostKeys[fence.Request.Grant.AgentID], request, time.Now().UTC())
	if err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb WHERE id='authority'", string(next)); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}
