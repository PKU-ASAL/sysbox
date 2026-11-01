package controlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// The authority and the source command are locked together. The validator is
// invoked inside that boundary; an expired/retired command cannot mint a grant.
type AuthorityStorage struct {
	Store         Store
	CommandID     string
	Validate      func(*persistence.CommandRecord) error
	WriteCommand  bool
	CommitCommand *bool
	Lifecycle     func(string) (*persistence.Repository, error)
	SourceErr     *error
}

func (b AuthorityStorage) ReadExecutionAuthority(ctx context.Context) ([]byte, error) {
	var raw []byte
	var err error
	switch s := b.Store.(type) {
	case *Local:
		raw, err = os.ReadFile(filepath.Join(s.RunsDir, "_execution-authority", "authority.json"))
		if os.IsNotExist(err) {
			err = persistence.ErrNotFound
		}
	case *SQLite:
		db, e := s.OpenDB()
		if e != nil {
			return nil, e
		}
		err = db.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			err = persistence.ErrNotFound
		}
	case *Postgres:
		pool, e := s.PoolFor(ctx)
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

func (b AuthorityStorage) UpdateExecutionAuthority(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	var sourceErr error
	b.SourceErr = &sourceErr
	allowed := true
	if b.CommitCommand == nil {
		b.CommitCommand = &allowed
	}
	if validate := b.Validate; validate != nil {
		b.Validate = func(record *persistence.CommandRecord) error {
			sourceErr = validate(record)
			if sourceErr != nil {
				*b.CommitCommand = false
			}
			return nil
		}
	}
	err := b.updateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		if sourceErr == nil {
			return fn(raw)
		}
		memory := &GrantTransaction{Raw: raw}
		if err := persistence.NewExecutionGrantRepository(memory).ObserveTime(ctx); err != nil {
			return nil, err
		}
		return memory.Raw, nil
	})
	if err != nil {
		return err
	}
	return sourceErr
}

func (b AuthorityStorage) updateExecutionAuthority(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	var current *persistence.CommandRecord
	switch store := b.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		if b.Validate != nil {
			record, err := (LocalCommandStorage{Store: store}).Read(ctx, b.CommandID)
			if err != nil {
				return err
			}
			if err = b.Validate(record); err != nil {
				return err
			}
			current = record
		}
		return persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			if err := b.CheckGuestSSHSource(current, func(id string) (*controlplane.NodeOperation, error) {
				return store.GetNodeOperation(ctx, id)
			}); err != nil {
				return nil, err
			}
			if err := b.checkGuestExecutionSource(current, func(id string) (*controlplane.GuestExecution, error) {
				return store.GetGuestExecution(ctx, id)
			}); err != nil {
				return nil, err
			}
			if err := b.checkConsoleSource(current, func(id string) (*controlplane.ConsoleSession, error) { return store.GetConsoleSession(ctx, id) }); err != nil {
				return nil, err
			}
			if err := b.checkLifecycle(current, func(topology string) error {
				repo, err := b.Lifecycle(topology)
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
			if b.WriteCommand && *b.CommitCommand && current != nil {
				data, err := persistence.EncodeCommandRecord(*current)
				if err != nil {
					return nil, err
				}
				path := filepath.Join(store.RunsDir, "_agents", current.Command.AgentID, "commands", current.Command.ID+".json")
				if err = WriteLocalObject(path, json.RawMessage(data)); err != nil {
					return nil, err
				}
			}
			return next, nil
		})
	case *SQLite:
		db, err := store.OpenDB()
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
		if b.Validate != nil {
			record, err := ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", b.CommandID))
			if err != nil {
				return err
			}
			if err = b.Validate(record); err != nil {
				return err
			}
			current = record
		}
		var raw []byte
		err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := b.CheckGuestSSHSource(current, func(id string) (*controlplane.NodeOperation, error) {
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
		if err := b.checkConsoleSource(current, func(id string) (*controlplane.ConsoleSession, error) {
			var data []byte
			if err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_console_sessions WHERE id=?", id).Scan(&data); err != nil {
				return nil, err
			}
			return decodeConsoleRecord(id, data)
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
		if b.WriteCommand && *b.CommitCommand && current != nil {
			if err = WriteSQLiteCommand(ctx, conn, *current, true); err != nil {
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
	case *Postgres:
		pool, err := store.PoolFor(ctx)
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
		raw, err := lockPostgresExecutionAuthority(ctx, tx)
		if err != nil {
			return err
		}
		if b.Validate != nil {
			record, err := ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", b.CommandID))
			if err != nil {
				return err
			}
			if err = b.Validate(record); err != nil {
				return err
			}
			current = record
		}
		// No operation row lock here: host completion locks authority before
		// operation/command. MVCC reads avoid adding another lock edge. Completion cannot
		// commit its result/release until this authority transaction finishes.
		if err := b.CheckGuestSSHSource(current, func(id string) (*controlplane.NodeOperation, error) {
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
		if err := b.checkConsoleSource(current, func(id string) (*controlplane.ConsoleSession, error) {
			var data []byte
			if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_console_sessions WHERE id=$1", id).Scan(&data); err != nil {
				return nil, err
			}
			return decodeConsoleRecord(id, data)
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
		if b.WriteCommand && *b.CommitCommand && current != nil {
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
