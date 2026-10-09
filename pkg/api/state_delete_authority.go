package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

// deleteStateWithAuthority keeps authority and lifecycle locked until the new
// topology identity commits. Operation-status preflight is not stop evidence;
// expired/revoked grants still exclude metadata deletion, including force.
func (s *Server) deleteStateWithAuthority(ctx context.Context, topology string, serial int64, uid string) error {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return err
	}
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		unlock, err := store.lockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		repo, err := s.actionRepository(topology)
		if err != nil {
			return err
		}
		return persistence.FilesystemExecutionGrantStorage(store.runsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			if err := checkLocalDeleteActivity(ctx, store, topology); err != nil {
				return nil, err
			}
			return nil, repo.DeleteQuiescentLifecycle(ctx, topology, serial, uid, authority)
		})
	case *sqliteAPIStore:
		return deleteSQLiteStateWithAuthority(ctx, store, topology, serial, uid)
	case *postgresAPIStore:
		return deletePostgresStateWithAuthority(ctx, store, topology, serial, uid)
	default:
		return persistence.ErrUnsupportedVersion
	}
}

func deleteSQLiteStateWithAuthority(ctx context.Context, store *sqliteAPIStore, topology string, serial int64, uid string) error {
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
	var authority, raw []byte
	err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority)
	if errors.Is(err, sql.ErrNoRows) {
		authority = nil
	} else if err != nil {
		return err
	} else if len(authority) == 0 {
		return persistence.ErrCorruptRecord
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&raw); err != nil {
		return err
	}
	if err := checkSQLiteDeleteActivity(ctx, conn, topology); err != nil {
		return err
	}
	next, err := persistence.ApplyQuiescentLifecycleDelete(raw, authority, topology, serial, uid)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, topology); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func deletePostgresStateWithAuthority(ctx context.Context, store *postgresAPIStore, topology string, serial int64, uid string) error {
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
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return err
	}
	// SELECT FOR UPDATE cannot lock an absent row. Initialize a valid empty
	// ledger inside this transaction so concurrent first issuance uses the same
	// exclusion row, even when this topology has never had an execution grant.
	initial := &grantTransaction{}
	if err = persistence.NewExecutionGrantRepository(initial).ObserveTime(ctx); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority',$1::jsonb) ON CONFLICT(id) DO NOTHING", string(initial.raw)); err != nil {
		return err
	}
	var authority, raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", topology).Scan(&raw); err != nil {
		return err
	}
	if err := checkPostgresDeleteActivity(ctx, tx, topology); err != nil {
		return err
	}
	next, err := persistence.ApplyQuiescentLifecycleDelete(raw, authority, topology, serial, uid)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), topology); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
