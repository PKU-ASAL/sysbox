package controlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) CancelGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return s.updateGuestExecution(ctx, id, persistence.PrepareGuestExecutionCancellation)
}

func (s *Local) FailUnissuedGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return s.updateGuestExecution(ctx, id, persistence.PrepareUnissuedGuestExecutionFailure)
}

func (s *Local) updateGuestExecution(ctx context.Context, id string, prepare persistence.GuestExecutionTransform) (*controlplane.GuestExecution, error) {
	if ValidatePathSegment(id, "execution") != nil {
		return nil, persistence.ErrInvalidRecord
	}
	unlock, err := s.LockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	var result *persistence.GuestExecutionMutation
	err = persistence.FilesystemExecutionGrantStorage(s.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
		var err error
		result, err = repo.UpdateFilesystemGuestExecutionUnderAuthority(ctx, id, authority, func(id string) (*persistence.CommandRecord, error) {
			return (LocalCommandStorage{Store: s}).Read(ctx, id)
		}, prepare)
		if err != nil {
			return nil, err
		}
		return result.Authority, nil
	})
	if err != nil {
		return nil, err
	}
	return &result.Execution, nil
}

func (s *SQLite) CancelGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return s.updateGuestExecution(ctx, id, persistence.PrepareGuestExecutionCancellation)
}

func (s *SQLite) FailUnissuedGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return s.updateGuestExecution(ctx, id, persistence.PrepareUnissuedGuestExecutionFailure)
}

func (s *SQLite) updateGuestExecution(ctx context.Context, id string, prepare persistence.GuestExecutionTransform) (*controlplane.GuestExecution, error) {
	if ValidatePathSegment(id, "execution") != nil {
		return nil, persistence.ErrInvalidRecord
	}
	db, err := s.OpenDB()
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
	var raw, authority []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_guest_executions WHERE id=?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	authority, err = readSQLiteAuthority(ctx, conn)
	if err != nil {
		return nil, err
	}
	prepared, err := prepare(id, raw, authority, func(commandID string) (*persistence.CommandRecord, error) {
		return ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", commandID))
	})
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_guest_executions SET data=? WHERE id=?", prepared.Record, id); err != nil {
		return nil, err
	}
	if prepared.Dispatch != nil {
		if err = WriteSQLiteCommand(ctx, conn, *prepared.Dispatch, false); err != nil {
			return nil, err
		}
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES('authority',?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", prepared.Authority); err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return &prepared.Execution, nil
}

func (s *Postgres) CancelGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return s.updateGuestExecution(ctx, id, persistence.PrepareGuestExecutionCancellation)
}

func (s *Postgres) FailUnissuedGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return s.updateGuestExecution(ctx, id, persistence.PrepareUnissuedGuestExecutionFailure)
}

func (s *Postgres) updateGuestExecution(ctx context.Context, id string, prepare persistence.GuestExecutionTransform) (*controlplane.GuestExecution, error) {
	if ValidatePathSegment(id, "execution") != nil {
		return nil, persistence.ErrInvalidRecord
	}
	pool, err := s.PoolFor(ctx)
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
	// Serialize authority first, then guest and command rows, matching completion.
	authority, err := lockPostgresExecutionAuthority(ctx, tx)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_guest_executions WHERE id=$1 FOR UPDATE", id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	prepared, err := prepare(id, raw, authority, func(commandID string) (*persistence.CommandRecord, error) {
		return ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", commandID))
	})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_guest_executions SET data=$1::jsonb,updated_at=now() WHERE id=$2", string(prepared.Record), id); err != nil {
		return nil, err
	}
	if prepared.Dispatch != nil {
		cmd := prepared.Dispatch.Command
		data, err := persistence.EncodeCommandRecord(*prepared.Dispatch)
		if err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, "INSERT INTO sysbox_agent_commands(id,agent_id,status,lease_owner,lease_until,attempt,data,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,now()) ON CONFLICT(id) DO NOTHING", cmd.ID, cmd.AgentID, cmd.Status, cmd.LeaseOwner, NullableTime(cmd.LeaseUntil), cmd.Attempt, string(data))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, persistence.ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(prepared.Authority)); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &prepared.Execution, nil
}
