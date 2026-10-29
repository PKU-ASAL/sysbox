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

type localConsoleStorage struct{ store *Local }

type sqliteConsoleStorage struct{ store *SQLite }

type postgresConsoleStorage struct{ store *Postgres }

func decodeConsoleRecord(id string, raw []byte) (*controlplane.ConsoleSession, error) {
	var sess *controlplane.ConsoleSession
	if json.Unmarshal(raw, &sess) != nil || sess == nil || sess.ID != id || ValidatePathSegment(id, "session") != nil {
		return nil, persistence.ErrCorruptRecord
	}
	return sess, nil
}

func (b localConsoleStorage) UpdateConsoleSession(ctx context.Context, id string, dispatch *controlplane.AgentCommand, transform func(*controlplane.ConsoleSession) (*controlplane.ConsoleSession, error)) error {
	unlock, err := b.store.LockRunRecords(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := b.store.GetConsoleSession(ctx, id)
	if err != nil && !errors.Is(err, ErrConsoleSessionNotFound) {
		return err
	}
	next, err := transform(current)
	if err != nil || next == nil {
		return err
	}
	raw, err := persistence.EncodeConsoleSession(*next)
	if err != nil {
		return err
	}
	workspace := next.Workspace
	if workspace == "" {
		workspace = next.Topology
	}
	target := filepath.Join(b.store.RunsDir, workspace, "sessions", id+".json")
	if current != nil {
		previous, readErr := os.ReadFile(target)
		if readErr != nil {
			return readErr
		}
		raw, err = persistence.PreserveConsoleDispatch(previous, *next)
		if err != nil {
			return err
		}
	}
	if dispatch != nil {
		prior, readErr := (LocalCommandStorage{Store: b.store}).Read(ctx, dispatch.ID)
		if readErr != nil {
			return readErr
		}
		prepared, prepareErr := prepareConsoleCommand(prior, *dispatch)
		if prepareErr != nil {
			return prepareErr
		}
		if prepared != nil {
			if dispatch.Type == "session_open" {
				raw, err = persistence.EncodeConsoleDispatch(*next, *dispatch)
			} else {
				raw, err = persistence.WithConsoleCancellation(raw, *dispatch)
			}
		}
	}
	if err != nil {
		return err
	}
	return persistence.FilesystemExecutionGrantStorage(b.store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
		if persistence.ConsoleSessionNeedsAdmission(current, *next) {
			if err := checkLocalTopologyMaintenance(ctx, b.store.RunsDir, authority, next.Topology); err != nil {
				return nil, err
			}
		}

		nextAuthority, err := persistence.PrepareConsoleAuthority(authority, next)
		if err != nil {
			return nil, err
		}
		raw, err = persistence.PreserveConsoleDispatch(raw, *next)
		if err != nil {
			return nil, err
		}
		return nextAuthority, WriteLocalObject(target, json.RawMessage(raw))
	})
}

func (b sqliteConsoleStorage) UpdateConsoleSession(ctx context.Context, id string, dispatch *controlplane.AgentCommand, transform func(*controlplane.ConsoleSession) (*controlplane.ConsoleSession, error)) error {
	db, err := b.store.OpenDB()
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
	var raw []byte
	err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_console_sessions WHERE id=?", id).Scan(&raw)
	var current *controlplane.ConsoleSession
	if err == nil {
		current, err = decodeConsoleRecord(id, raw)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return err
	}
	next, err := transform(current)
	if err != nil {
		return err
	}
	if next != nil {
		authority, err := readSQLiteAuthority(ctx, conn)
		if err != nil {
			return err
		}
		authority, err = persistence.PrepareConsoleAuthority(authority, next)
		if err != nil {
			return err
		}
		if authority != nil {
			if _, err = conn.ExecContext(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority',?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", authority); err != nil {
				return err
			}
		}
		if persistence.ConsoleSessionNeedsAdmission(current, *next) {
			if err := checkSQLiteTopologyMaintenance(ctx, conn, next.Topology); err != nil {
				return err
			}
		}
		raw, err = persistence.EncodeConsoleSession(*next)
		if err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO sysbox_console_sessions(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, raw); err != nil {
			return err
		}
		if dispatch != nil {
			prior, err := ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", dispatch.ID))
			if err != nil {
				return err
			}
			prepared, err := prepareConsoleCommand(prior, *dispatch)
			if err != nil {
				return err
			}
			if prepared != nil {
				if err = WriteSQLiteCommand(ctx, conn, *prepared, false); err != nil {
					return err
				}
			}
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func (b postgresConsoleStorage) UpdateConsoleSession(ctx context.Context, id string, dispatch *controlplane.AgentCommand, transform func(*controlplane.ConsoleSession) (*controlplane.ConsoleSession, error)) error {
	pool, err := b.store.PoolFor(ctx)
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
	// Materialize and lock authority before reading the session, including for
	// the first admission. A SELECT FOR UPDATE on an absent row is not a lock.
	initial := &GrantTransaction{}
	if err := persistence.NewExecutionGrantRepository(initial).ObserveTime(ctx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority',$1::jsonb) ON CONFLICT(id) DO NOTHING", string(initial.Raw)); err != nil {
		return err
	}
	var authority []byte
	if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return err
	}
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_console_sessions WHERE id=$1 FOR UPDATE", id).Scan(&raw)
	var current *controlplane.ConsoleSession
	if err == nil {
		current, err = decodeConsoleRecord(id, raw)
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return err
	}
	next, err := transform(current)
	if err != nil {
		return err
	}
	if next != nil {
		authority, err = persistence.PrepareConsoleAuthority(authority, next)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(authority)); err != nil {
			return err
		}
		if persistence.ConsoleSessionNeedsAdmission(current, *next) {
			if err := checkPostgresTopologyMaintenance(ctx, tx, authority, next.Topology); err != nil {
				return err
			}
		}
		raw, err = persistence.EncodeConsoleSession(*next)
		if err != nil {
			return err
		}
		workspace := next.Workspace
		if workspace == "" {
			workspace = next.Topology
		}
		if _, err = tx.Exec(ctx, "INSERT INTO sysbox_console_sessions(workspace,id,data,updated_at) VALUES($1,$2,$3::jsonb,now()) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=now()", workspace, id, string(raw)); err != nil {
			return err
		}
		if dispatch != nil {
			prior, err := ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", dispatch.ID))
			if err != nil {
				return err
			}
			prepared, err := prepareConsoleCommand(prior, *dispatch)
			if err != nil {
				return err
			}
			if prepared == nil {
				return tx.Commit(ctx)
			}
			command, err := persistence.EncodeCommandRecord(*prepared)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, "INSERT INTO sysbox_agent_commands(id,agent_id,status,lease_owner,lease_until,attempt,data,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,now()) ON CONFLICT(id) DO NOTHING", dispatch.ID, dispatch.AgentID, dispatch.Status, dispatch.LeaseOwner, NullableTime(dispatch.LeaseUntil), dispatch.Attempt, string(command))
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return persistence.ErrConflict
			}
		}
	}
	return tx.Commit(ctx)
}

func prepareConsoleCommand(prior *persistence.CommandRecord, cmd controlplane.AgentCommand) (*persistence.CommandRecord, error) {
	if cmd.Type == "session_open" && prior != nil {
		return nil, persistence.ErrConflict
	}
	return persistence.PrepareCommandEnqueue(prior, cmd)
}
