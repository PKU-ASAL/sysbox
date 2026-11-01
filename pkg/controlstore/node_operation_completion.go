package controlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func ValidateHostOperationReceipt(ctx context.Context, store Store, receipt controlplane.SignedExecutionStopReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	return persistence.NewExecutionGrantRepository(AuthorityStorage{Store: store}).ValidateHostStop(ctx, receipt.Grant, fmt.Sprintf("%x", sum[:]))
}

func (s *Local) CompleteSSHAccessFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := ValidateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.TransitionNodeOperation(ctx, agent, id, &receipt, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteSSHAccessFromHost(current, command, agent, id, receipt)
	})
}

func (s *Local) CompleteAdoptionFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := ValidateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.TransitionNodeOperation(ctx, agent, id, &receipt, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionFromHost(current, command, agent, id, receipt)
	})
}

func (s *Local) TransitionNodeOperation(ctx context.Context, agent, id string, receipt *controlplane.SignedExecutionStopReceipt, transition func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
	unlock, err := s.LockRunRecords(ctx)
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
	command, err := (LocalCommandStorage{Store: s}).Read(ctx, current.CommandID)
	if err != nil {
		return nil, err
	}
	completed, err := repo.CompleteWithTransition(ctx, id, func(current controlplane.NodeOperation) (controlplane.NodeOperation, error) {
		return transition(current, command)
	})
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		rawReceipt, e := json.Marshal(*receipt)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(rawReceipt)
		storage := persistence.FilesystemExecutionGrantStorage(s.RunsDir)
		if e = storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			return persistence.ApplyHostStopAuthority(raw, receipt.Grant, fmt.Sprintf("%x", sum[:]))
		}); e != nil {
			return nil, e
		}
	}
	return completed, nil
}

func (s *SQLite) CompleteSSHAccessFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := ValidateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.TransitionNodeOperation(ctx, agent, id, &receipt, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteSSHAccessFromHost(current, command, agent, id, receipt)
	})
}

func (s *SQLite) CompleteAdoptionFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := ValidateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.TransitionNodeOperation(ctx, agent, id, &receipt, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionFromHost(current, command, agent, id, receipt)
	})
}

func (s *SQLite) TransitionNodeOperation(ctx context.Context, agent, id string, receipt *controlplane.SignedExecutionStopReceipt, transition func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
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
	command, err := ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", current.CommandID))
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
	if receipt != nil {
		rawReceipt, e := json.Marshal(*receipt)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(rawReceipt)
		authority, e := readSQLiteAuthority(ctx, conn)
		if e != nil {
			return nil, e
		}
		nextAuthority, e := persistence.ApplyHostStopAuthority(authority, receipt.Grant, fmt.Sprintf("%x", sum[:]))
		if e != nil {
			return nil, e
		}
		if _, e = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", nextAuthority); e != nil {
			return nil, e
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return &completed, nil
}

func (s *Postgres) CompleteSSHAccessFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := ValidateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.TransitionNodeOperation(ctx, agent, id, &receipt, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteSSHAccessFromHost(current, command, agent, id, receipt)
	})
}

func (s *Postgres) CompleteAdoptionFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error) {
	if err := ValidateHostOperationReceipt(ctx, s, receipt); err != nil {
		return nil, err
	}
	return s.TransitionNodeOperation(ctx, agent, id, &receipt, func(current controlplane.NodeOperation, command *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionFromHost(current, command, agent, id, receipt)
	})
}

func (s *Postgres) TransitionNodeOperation(ctx context.Context, agent, id string, receipt *controlplane.SignedExecutionStopReceipt, transition func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
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
	// Host completion must acquire authority before any operation/command
	// locks. Grant issuance holds authority before command; the reverse order
	// allows completion and renewal to deadlock. Keep this same authority
	// snapshot locked through the result and stop-receipt commit.
	var authority []byte
	if receipt != nil {
		if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, persistence.ErrNotFound
			}
			return nil, err
		}
	}
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
	command, err := ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", current.CommandID))
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
	if receipt != nil {
		rawReceipt, e := json.Marshal(*receipt)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(rawReceipt)
		nextAuthority, e := persistence.ApplyHostStopAuthority(authority, receipt.Grant, fmt.Sprintf("%x", sum[:]))
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(nextAuthority)); e != nil {
			return nil, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &completed, nil
}
