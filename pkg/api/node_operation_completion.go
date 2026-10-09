package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func validateHostOperationReceipt(ctx context.Context, store apiStore, receipt controlplane.SignedExecutionStopReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	return persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).ValidateHostStop(ctx, receipt.Grant, fmt.Sprintf("%x", sum[:]))
}

func (s *localAPIStore) CompleteNodeOperation(ctx context.Context, agent, id string, report controlplane.NodeOperation) (*controlplane.NodeOperation, error) {
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteNodeOperation(current, command, agent, id, report, time.Now().UTC())
	})
}
func (s *localAPIStore) CompleteSSHAccessFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := validateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteSSHAccessFromHost(current, command, agent, id, receipt)
	})
}

func (s *localAPIStore) CompleteAdoptionFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := validateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionFromHost(current, command, agent, id, receipt)
	})
}
func (s *localAPIStore) completeNodeOperation(ctx context.Context, agent, id string, transition func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
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
	return repo.CompleteWithTransition(ctx, id, func(current controlplane.NodeOperation) (controlplane.NodeOperation, error) {
		return transition(current, command)
	})
}

func (s *sqliteAPIStore) CompleteNodeOperation(ctx context.Context, agent, id string, report controlplane.NodeOperation) (*controlplane.NodeOperation, error) {
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteNodeOperation(current, command, agent, id, report, time.Now().UTC())
	})
}
func (s *sqliteAPIStore) CompleteSSHAccessFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := validateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteSSHAccessFromHost(current, command, agent, id, receipt)
	})
}

func (s *sqliteAPIStore) CompleteAdoptionFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := validateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionFromHost(current, command, agent, id, receipt)
	})
}
func (s *sqliteAPIStore) completeNodeOperation(ctx context.Context, agent, id string, transition func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
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
	completed, err := transition(*current, command)
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
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteNodeOperation(current, command, agent, id, report, time.Now().UTC())
	})
}
func (s *postgresAPIStore) CompleteSSHAccessFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := validateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteSSHAccessFromHost(current, command, agent, id, receipt)
	})
}

func (s *postgresAPIStore) CompleteAdoptionFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := validateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.completeNodeOperation(ctx, agent, id, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionFromHost(current, command, agent, id, receipt)
	})
}
func (s *postgresAPIStore) completeNodeOperation(ctx context.Context, agent, id string, transition func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
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
	completed, err := transition(*current, command)
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
