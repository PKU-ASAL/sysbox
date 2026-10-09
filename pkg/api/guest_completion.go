package api

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func (s *localAPIStore) CompleteFileFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (controlplane.GuestFileOperation, error) {
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{HostFile: &receipt})
	if err != nil {
		return controlplane.GuestFileOperation{}, err
	}
	return persistence.DecodeCompletedGuestFile(raw)
}
func (s *sqliteAPIStore) CompleteFileFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (controlplane.GuestFileOperation, error) {
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{HostFile: &receipt})
	if err != nil {
		return controlplane.GuestFileOperation{}, err
	}
	return persistence.DecodeCompletedGuestFile(raw)
}
func (s *postgresAPIStore) CompleteFileFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (controlplane.GuestFileOperation, error) {
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{HostFile: &receipt})
	if err != nil {
		return controlplane.GuestFileOperation{}, err
	}
	return persistence.DecodeCompletedGuestFile(raw)
}

func (s *localAPIStore) completeGuest(ctx context.Context, agent, id string, report persistence.GuestCompletion) ([]byte, error) {
	unlock, err := s.lockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	if report.HostExecution != nil {
		storage := persistence.FilesystemExecutionGrantStorage(s.runsDir)
		authority, readErr := storage.ReadExecutionAuthority(ctx)
		if readErr != nil {
			return nil, readErr
		}
		var result *persistence.GuestExecutionMutation
		result, err = repo.UpdateFilesystemGuestExecutionUnderAuthority(ctx, id, authority, func(commandID string) (*persistence.CommandRecord, error) {
			return (localCommandStorage{store: s}).read(ctx, commandID)
		}, func(id string, raw, authority []byte, lookup func(string) (*persistence.CommandRecord, error)) (*persistence.GuestExecutionMutation, error) {
			return persistence.PrepareGuestExecutionHostCompletion(id, raw, authority, lookup, agent, *report.HostExecution)
		})
		if err != nil {
			return nil, err
		}
		return result.Record, nil
	}
	var commandID string
	if report.HostExecution != nil {
		x, err := repo.GetGuestExecution(ctx, id)
		if err != nil {
			return nil, err
		}
		commandID = x.CommandID
	} else {
		x, err := repo.GetGuestFileOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		commandID = x.CommandID
	}
	command, err := (localCommandStorage{store: s}).read(ctx, commandID)
	if err != nil {
		return nil, err
	}
	return repo.CompleteUnderCommandLock(ctx, agent, id, report, command)
}
func (s *sqliteAPIStore) completeGuest(ctx context.Context, agent, id string, report persistence.GuestCompletion) ([]byte, error) {
	table, err := report.Table()
	if err != nil {
		return nil, err
	}
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
				_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
			}
		}
	}()
	var raw []byte
	if err := conn.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	_, commandID, err := report.Binding(raw, id)
	if err != nil {
		return nil, err
	}
	command, err := scanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", commandID))
	if err != nil {
		return nil, err
	}
	var next []byte
	var prepared *persistence.GuestExecutionMutation
	if report.HostExecution != nil {
		authority, readErr := readSQLiteAuthority(ctx, conn)
		if readErr != nil {
			return nil, readErr
		}
		prepared, err = persistence.PrepareGuestExecutionHostCompletion(id, raw, authority, func(string) (*persistence.CommandRecord, error) { return command, nil }, agent, *report.HostExecution)
		if err != nil {
			return nil, err
		}
		next = prepared.Record
	} else {
		next, err = persistence.CompleteGuestRecord(raw, command, agent, id, report, time.Now().UTC())
	}
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE "+table+" SET data=? WHERE id=?", next, id); err != nil {
		return nil, err
	}
	if prepared != nil {
		if _, err = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", prepared.Authority); err != nil {
			return nil, err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return next, nil
}

func (s *postgresAPIStore) completeGuest(ctx context.Context, agent, id string, report persistence.GuestCompletion) ([]byte, error) {
	table, err := report.Table()
	if err != nil {
		return nil, err
	}
	pool, err := s.poolFor(ctx)
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
	if err := tx.QueryRow(ctx, "SELECT data::text FROM "+table+" WHERE id=$1 FOR UPDATE", id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	_, commandID, err := report.Binding(raw, id)
	if err != nil {
		return nil, err
	}
	command, err := scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", commandID))
	if err != nil {
		return nil, err
	}
	var next []byte
	var prepared *persistence.GuestExecutionMutation
	if report.HostExecution != nil {
		var authority []byte
		if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
			return nil, err
		}
		prepared, err = persistence.PrepareGuestExecutionHostCompletion(id, raw, authority, func(string) (*persistence.CommandRecord, error) { return command, nil }, agent, *report.HostExecution)
		if err != nil {
			return nil, err
		}
		next = prepared.Record
	} else {
		next, err = persistence.CompleteGuestRecord(raw, command, agent, id, report, time.Now().UTC())
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE "+table+" SET data=$1::jsonb,updated_at=now() WHERE id=$2", string(next), id); err != nil {
		return nil, err
	}
	if prepared != nil {
		if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(prepared.Authority)); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

func (s *localAPIStore) CompleteGuestExecutionFromHost(ctx context.Context, agent, id string, r controlplane.SignedExecutionStopReceipt) (controlplane.GuestExecution, error) {
	if r.GuestResult == nil {
		return controlplane.GuestExecution{}, persistence.ErrConflict
	}
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{HostExecution: &r})
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	return persistence.DecodeCompletedGuestExecution(raw)
}
func (s *localAPIStore) RecoverGuestExecutionHostCompletion(ctx context.Context, id string) (controlplane.GuestExecution, error) {
	unlock, err := s.lockRunRecords(ctx)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	defer unlock()
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	var result *persistence.GuestExecutionMutation
	err = persistence.FilesystemExecutionGrantStorage(s.runsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
		result, err = repo.UpdateFilesystemGuestExecutionUnderAuthority(ctx, id, authority, nil, func(id string, raw, authority []byte, _ func(string) (*persistence.CommandRecord, error)) (*persistence.GuestExecutionMutation, error) {
			return persistence.RecoverGuestExecutionHostCompletion(id, raw, authority)
		})
		if err != nil {
			return nil, err
		}
		return result.Authority, nil
	})
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	return result.Execution, nil
}
func (s *localAPIStore) CompleteGuestFileOperation(ctx context.Context, agent, id string, c controlplane.GuestFileOperationCompletion) (controlplane.GuestFileOperation, error) {
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{File: &c})
	if err != nil {
		return controlplane.GuestFileOperation{}, err
	}
	return persistence.DecodeCompletedGuestFile(raw)
}

func (s *sqliteAPIStore) CompleteGuestExecutionFromHost(ctx context.Context, agent, id string, r controlplane.SignedExecutionStopReceipt) (controlplane.GuestExecution, error) {
	if r.GuestResult == nil {
		return controlplane.GuestExecution{}, persistence.ErrConflict
	}
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{HostExecution: &r})
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	return persistence.DecodeCompletedGuestExecution(raw)
}
func (s *sqliteAPIStore) RecoverGuestExecutionHostCompletion(ctx context.Context, id string) (controlplane.GuestExecution, error) {
	db, err := s.open()
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return controlplane.GuestExecution{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var raw, authority []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_guest_executions WHERE id=?", id).Scan(&raw); err != nil {
		return controlplane.GuestExecution{}, err
	}
	authority, err = readSQLiteAuthority(ctx, conn)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	prepared, err := persistence.RecoverGuestExecutionHostCompletion(id, raw, authority)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_guest_executions SET data=? WHERE id=?", prepared.Record, id); err != nil {
		return controlplane.GuestExecution{}, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", prepared.Authority); err != nil {
		return controlplane.GuestExecution{}, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return controlplane.GuestExecution{}, err
	}
	committed = true
	return prepared.Execution, nil
}
func (s *sqliteAPIStore) CompleteGuestFileOperation(ctx context.Context, agent, id string, c controlplane.GuestFileOperationCompletion) (controlplane.GuestFileOperation, error) {
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{File: &c})
	if err != nil {
		return controlplane.GuestFileOperation{}, err
	}
	return persistence.DecodeCompletedGuestFile(raw)
}

func (s *postgresAPIStore) CompleteGuestExecutionFromHost(ctx context.Context, agent, id string, r controlplane.SignedExecutionStopReceipt) (controlplane.GuestExecution, error) {
	if r.GuestResult == nil {
		return controlplane.GuestExecution{}, persistence.ErrConflict
	}
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{HostExecution: &r})
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	return persistence.DecodeCompletedGuestExecution(raw)
}
func (s *postgresAPIStore) RecoverGuestExecutionHostCompletion(ctx context.Context, id string) (controlplane.GuestExecution, error) {
	pool, err := s.poolFor(ctx)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	defer tx.Rollback(ctx)
	var raw, authority string
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_guest_executions WHERE id=$1 FOR UPDATE", id).Scan(&raw); err != nil {
		return controlplane.GuestExecution{}, err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return controlplane.GuestExecution{}, err
	}
	prepared, err := persistence.RecoverGuestExecutionHostCompletion(id, []byte(raw), []byte(authority))
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_guest_executions SET data=$1::jsonb,updated_at=now() WHERE id=$2", string(prepared.Record), id); err != nil {
		return controlplane.GuestExecution{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(prepared.Authority)); err != nil {
		return controlplane.GuestExecution{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.GuestExecution{}, err
	}
	return prepared.Execution, nil
}
func (s *postgresAPIStore) CompleteGuestFileOperation(ctx context.Context, agent, id string, c controlplane.GuestFileOperationCompletion) (controlplane.GuestFileOperation, error) {
	raw, err := s.completeGuest(ctx, agent, id, persistence.GuestCompletion{File: &c})
	if err != nil {
		return controlplane.GuestFileOperation{}, err
	}
	return persistence.DecodeCompletedGuestFile(raw)
}
