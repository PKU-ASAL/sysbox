package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"os"
	"path/filepath"
	"time"
)

// The authority and the source command are locked together. The validator is
// invoked inside that boundary; an expired/retired command cannot mint a grant.
type apiExecutionGrantStorage struct {
	store         apiStore
	commandID     string
	validate      func(*persistence.CommandRecord) error
	writeCommand  bool
	commitCommand *bool
	lifecycle     func(string) (*persistence.Repository, error)
	sourceErr     *error
}

func (b apiExecutionGrantStorage) ReadExecutionAuthority(ctx context.Context) ([]byte, error) {
	var raw []byte
	var err error
	switch s := b.store.(type) {
	case *localAPIStore:
		raw, err = os.ReadFile(filepath.Join(s.runsDir, "_execution-authority", "authority.json"))
		if os.IsNotExist(err) {
			err = persistence.ErrNotFound
		}
	case *sqliteAPIStore:
		db, e := s.open()
		if e != nil {
			return nil, e
		}
		err = db.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			err = persistence.ErrNotFound
		}
	case *postgresAPIStore:
		pool, e := s.poolFor(ctx)
		if e != nil {
			return nil, e
		}
		err = pool.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			err = persistence.ErrNotFound
		}
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
	return raw, err
}

func (b apiExecutionGrantStorage) UpdateExecutionAuthority(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	var sourceErr error
	b.sourceErr = &sourceErr
	allowed := true
	if b.commitCommand == nil {
		b.commitCommand = &allowed
	}
	if validate := b.validate; validate != nil {
		b.validate = func(record *persistence.CommandRecord) error {
			sourceErr = validate(record)
			if sourceErr != nil {
				*b.commitCommand = false
			}
			return nil
		}
	}
	err := b.updateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		if sourceErr == nil {
			return fn(raw)
		}
		memory := &grantTransaction{raw: raw}
		if err := persistence.NewExecutionGrantRepository(memory).ObserveTime(ctx); err != nil {
			return nil, err
		}
		return memory.raw, nil
	})
	if err != nil {
		return err
	}
	return sourceErr
}

func (b apiExecutionGrantStorage) updateExecutionAuthority(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	var current *persistence.CommandRecord
	switch store := b.store.(type) {
	case *localAPIStore:
		unlock, err := store.lockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		if b.validate != nil {
			record, err := (localCommandStorage{store: store}).read(ctx, b.commandID)
			if err != nil {
				return err
			}
			if err = b.validate(record); err != nil {
				return err
			}
			current = record
		}
		return persistence.FilesystemExecutionGrantStorage(store.runsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			if err := b.checkGuestSSHSource(current, func(id string) (*controlplane.NodeOperation, error) {
				return store.GetNodeOperation(ctx, id)
			}); err != nil {
				return nil, err
			}
			if err := b.checkGuestExecutionSource(current, func(id string) (*controlplane.GuestExecution, error) {
				return store.GetGuestExecution(ctx, id)
			}); err != nil {
				return nil, err
			}
			if err := b.checkLifecycle(current, func(topology string) error {
				repo, err := b.lifecycle(topology)
				if err != nil {
					return err
				}
				return repo.ValidateCommandLifecycle(ctx, topology, current.Command)
			}); err != nil {
				return nil, err
			}
			next, err := fn(raw)
			if err != nil {
				return nil, err
			}
			// Command extension is committed first. If the authority write fails,
			// no extended signed grant escapes; retry can safely finish renewal.
			if b.writeCommand && *b.commitCommand && current != nil {
				data, err := persistence.EncodeCommandRecord(*current)
				if err != nil {
					return nil, err
				}
				path := filepath.Join(store.runsDir, "_agents", current.Command.AgentID, "commands", current.Command.ID+".json")
				if err = writeLocalObject(path, json.RawMessage(data)); err != nil {
					return nil, err
				}
			}
			return next, nil
		})
	case *sqliteAPIStore:
		db, err := store.open()
		if err != nil {
			return err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			return err
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
		if b.validate != nil {
			record, err := scanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", b.commandID))
			if err != nil {
				return err
			}
			if err = b.validate(record); err != nil {
				return err
			}
			current = record
		}
		var raw []byte
		err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := b.checkGuestSSHSource(current, func(id string) (*controlplane.NodeOperation, error) {
			var data []byte
			if err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_node_operations WHERE id=?", id).Scan(&data); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, persistence.ErrNotFound
				}
				return nil, err
			}
			return persistence.DecodeNodeOperation(data)
		}); err != nil {
			return err
		}
		if err := b.checkGuestExecutionSource(current, func(id string) (*controlplane.GuestExecution, error) {
			var data []byte
			if err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_guest_executions WHERE id=?", id).Scan(&data); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, persistence.ErrNotFound
				}
				return nil, err
			}
			x, err := persistence.DecodeGuestExecution(id, data)
			return &x, err
		}); err != nil {
			return err
		}
		if err := b.checkLifecycle(current, func(topology string) error {
			var lifecycle []byte
			if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&lifecycle); errors.Is(err, sql.ErrNoRows) {
				return persistence.ErrConflict
			} else if err != nil {
				return err
			}
			return persistence.ValidateCommandLifecycle(lifecycle, topology, current.Command)
		}); err != nil {
			return err
		}
		next, err := fn(raw)
		if err != nil {
			return err
		}
		if b.writeCommand && *b.commitCommand && current != nil {
			if err = writeSQLiteCommand(ctx, conn, *current, true); err != nil {
				return err
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority',?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", next); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
		return nil
	case *postgresAPIStore:
		pool, err := store.poolFor(ctx)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(cleanup)
		}()
		tag, err := tx.Exec(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority','{}'::jsonb) ON CONFLICT(id) DO NOTHING")
		if err != nil {
			return err
		}
		var raw []byte
		if tag.RowsAffected() == 0 {
			if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&raw); err != nil {
				return err
			}
		}
		if b.validate != nil {
			record, err := scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", b.commandID))
			if err != nil {
				return err
			}
			if err = b.validate(record); err != nil {
				return err
			}
			current = record
		}
		// No operation row lock here: host completion locks authority before
		// operation/command. MVCC reads avoid adding another lock edge. Completion cannot
		// commit its result/release until this authority transaction finishes.
		if err := b.checkGuestSSHSource(current, func(id string) (*controlplane.NodeOperation, error) {
			var data []byte
			if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_node_operations WHERE id=$1", id).Scan(&data); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, persistence.ErrNotFound
				}
				return nil, err
			}
			return persistence.DecodeNodeOperation(data)
		}); err != nil {
			return err
		}
		if err := b.checkGuestExecutionSource(current, func(id string) (*controlplane.GuestExecution, error) {
			var data []byte
			if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_guest_executions WHERE id=$1", id).Scan(&data); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, persistence.ErrNotFound
				}
				return nil, err
			}
			x, err := persistence.DecodeGuestExecution(id, data)
			return &x, err
		}); err != nil {
			return err
		}
		if err := b.checkLifecycle(current, func(topology string) error {
			var lifecycle []byte
			if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", topology).Scan(&lifecycle); errors.Is(err, pgx.ErrNoRows) {
				return persistence.ErrConflict
			} else if err != nil {
				return err
			}
			return persistence.ValidateCommandLifecycle(lifecycle, topology, current.Command)
		}); err != nil {
			return err
		}
		next, err := fn(raw)
		if err != nil {
			return err
		}
		if b.writeCommand && *b.commitCommand && current != nil {
			data, err := persistence.EncodeCommandRecord(*current)
			if err != nil {
				return err
			}
			cmd := current.Command
			if _, err = tx.Exec(ctx, "UPDATE sysbox_agent_commands SET lease_until=$2,data=$3::jsonb,updated_at=now() WHERE id=$1", cmd.ID, cmd.LeaseUntil, string(data)); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(next)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	default:
		return persistence.ErrUnsupportedVersion
	}
}
