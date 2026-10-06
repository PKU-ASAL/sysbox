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

func (s *localAPIStore) CompleteNodeOperation(ctx context.Context, agent, id string, report controlplane.NodeOperation) (*controlplane.NodeOperation, error) {
	unlock, err := s.lockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return nil, err
	}
	current, err := repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	command, err := (localCommandStorage{store: s}).read(ctx, current.CommandID)
	if err != nil {
		return nil, err
	}
	return repo.CompleteUnderCommandLock(ctx, agent, id, report, command)
}

func (s *sqliteAPIStore) CompleteNodeOperation(ctx context.Context, agent, id string, report controlplane.NodeOperation) (*controlplane.NodeOperation, error) {
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
	if err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_node_operations WHERE id=?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	current, err := persistence.DecodeNodeOperation(raw)
	if err != nil {
		return nil, err
	}
	command, err := scanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", current.CommandID))
	if err != nil {
		return nil, err
	}
	completed, err := persistence.CompleteNodeOperation(*current, command, agent, id, report, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	next, err := persistence.EncodeNodeOperation(completed)
	if err != nil {
		return nil, err
	}
	next, err = persistence.PreserveNodeOperationDispatch(raw, next)
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_node_operations SET data=? WHERE id=?", next, id); err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return &completed, nil
}

func (s *postgresAPIStore) CompleteNodeOperation(ctx context.Context, agent, id string, report controlplane.NodeOperation) (*controlplane.NodeOperation, error) {
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
	if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_node_operations WHERE id=$1 FOR UPDATE", id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, persistence.ErrNotFound
		}
		return nil, err
	}
	current, err := persistence.DecodeNodeOperation(raw)
	if err != nil {
		return nil, err
	}
	command, err := scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", current.CommandID))
	if err != nil {
		return nil, err
	}
	completed, err := persistence.CompleteNodeOperation(*current, command, agent, id, report, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	next, err := persistence.EncodeNodeOperation(completed)
	if err != nil {
		return nil, err
	}
	next, err = persistence.PreserveNodeOperationDispatch(raw, next)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_node_operations SET data=$1::jsonb,updated_at=now() WHERE id=$2", string(next), id); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &completed, nil
}
