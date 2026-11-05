package controlstore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// RecoverUnclaimedRunDelivery uses the same admission/authority/record order as
// claim and grant issuance. No pre-read is trusted across this boundary.
func (s *Transactions) RecoverUnclaimedRunDelivery(ctx context.Context, id string) error {
	if err := ValidatePathSegment(id, "command"); err != nil {
		return err
	}
	switch store := s.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		record, err := (LocalCommandStorage{Store: store}).Read(ctx, id)
		if err != nil {
			return err
		}
		if record == nil || record.Command.Run == nil {
			return nil
		}
		run, err := store.GetRun(ctx, record.Command.Run.ID)
		if err != nil {
			return err
		}
		return persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			next, err := persistence.RecoverUnclaimedDelivery(run, record, raw, time.Now().UTC())
			if err != nil || next == nil {
				return nil, err
			}
			encoded, err := persistence.EncodeCommandRecord(*next)
			if err != nil {
				return nil, err
			}
			return nil, WriteLocalObject(filepath.Join(store.RunsDir, "_agents", next.Command.AgentID, "commands", id+".json"), json.RawMessage(encoded))
		})
	case *SQLite:
		return recoverSQLiteUnclaimed(ctx, store, id)
	case *Postgres:
		return recoverPostgresUnclaimed(ctx, store, id)
	default:
		return persistence.ErrUnsupportedVersion
	}
}
func recoverSQLiteUnclaimed(ctx context.Context, store *SQLite, id string) error {
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
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := conn.ExecContext(c, "ROLLBACK"); e != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	record, err := ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", id))
	if err != nil {
		return err
	}
	if record != nil && record.Command.Run != nil {
		run, err := ScanSQLiteRun(conn.QueryRowContext(ctx, "SELECT "+SqliteRunColumns+" FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1", record.Command.Run.ID))
		if err != nil {
			return err
		}
		authority, err := readSQLiteAuthority(ctx, conn)
		if err != nil {
			return err
		}
		next, err := persistence.RecoverUnclaimedDelivery(run, record, authority, time.Now().UTC())
		if err != nil {
			return err
		}
		if next != nil {
			if err = WriteSQLiteCommand(ctx, conn, *next, true); err != nil {
				return err
			}
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
func recoverPostgresUnclaimed(ctx context.Context, store *Postgres, id string) error {
	pool, err := store.PoolFor(ctx)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return err
	}
	authority, err := lockPostgresExecutionAuthority(ctx, tx)
	if err != nil {
		return err
	}
	// Authority is held before either row, as in grant issuance. Read the
	// immutable Run ID first, then acquire Run -> command row locks, matching
	// claim/completion and avoiding an inverse row order.
	record, err := ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1", id))
	if err != nil {
		return err
	}
	if record != nil && record.Command.Run != nil {
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_runs WHERE id=$1 FOR UPDATE", record.Command.Run.ID).Scan(&raw); err != nil {
			return err
		}
		var run controlplane.Run
		if err = json.Unmarshal(raw, &run); err != nil {
			return err
		}
		controlplane.NormalizeRun(&run)
		record, err = ScanPostgresCommand(tx.QueryRow(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		next, err := persistence.RecoverUnclaimedDelivery(&run, record, authority, time.Now().UTC())
		if err != nil {
			return err
		}
		if next != nil {
			raw, err = persistence.EncodeCommandRecord(*next)
			if err != nil {
				return err
			}
			c := next.Command
			if _, err = tx.Exec(ctx, "UPDATE sysbox_agent_commands SET status=$2,lease_owner=$3,lease_until=$4,attempt=$5,data=$6::jsonb,updated_at=now() WHERE id=$1", id, c.Status, c.LeaseOwner, NullableTime(c.LeaseUntil), c.Attempt, string(raw)); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
