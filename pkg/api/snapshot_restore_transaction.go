package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

// commitSnapshotRestore keeps authority and aggregate locked through the state
// and receipt write. It requires an already-persisted barrier; it cannot create
// authority, release a barrier, or replace the workflow's activity checks.
func (s *Server) commitSnapshotRestore(ctx context.Context, topology string, request persistence.SnapshotRestoreRequest) (persistence.SnapshotRestoreReceipt, error) {
	var zero persistence.SnapshotRestoreReceipt
	if err := validatePathSegment(topology, "topology"); err != nil {
		return zero, err
	}
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		unlock, err := store.lockRunRecords(ctx)
		if err != nil {
			return zero, err
		}
		defer unlock()
		repo, err := s.actionRepository(topology)
		if err != nil {
			return zero, err
		}
		var receipt persistence.SnapshotRestoreReceipt
		err = persistence.FilesystemExecutionGrantStorage(store.runsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			var err error
			receipt, err = repo.CommitAuthorizedSnapshotRestore(ctx, topology, request, authority)
			return nil, err // Keep the authority lock, but do not rewrite its ledger.
		})
		if err != nil {
			return zero, err
		}
		return receipt, nil
	case *sqliteAPIStore:
		return commitSQLiteSnapshotRestore(ctx, store, topology, request)
	case *postgresAPIStore:
		return commitPostgresSnapshotRestore(ctx, store, topology, request)
	default:
		return zero, persistence.ErrUnsupportedVersion
	}
}

func commitSQLiteSnapshotRestore(ctx context.Context, store *sqliteAPIStore, topology string, request persistence.SnapshotRestoreRequest) (persistence.SnapshotRestoreReceipt, error) {
	var zero persistence.SnapshotRestoreReceipt
	db, err := store.open()
	if err != nil {
		return zero, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return zero, err
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
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority); err != nil {
		return zero, err
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&raw); err != nil {
		return zero, err
	}
	next, receipt, err := persistence.ApplyAuthorizedSnapshotRestore(raw, authority, topology, request)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(raw, next) {
		if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, topology); err != nil {
			return zero, err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return zero, err
	}
	committed = true
	return receipt, nil
}

func commitPostgresSnapshotRestore(ctx context.Context, store *postgresAPIStore, topology string, request persistence.SnapshotRestoreRequest) (persistence.SnapshotRestoreReceipt, error) {
	var zero persistence.SnapshotRestoreReceipt
	pool, err := store.poolFor(ctx)
	if err != nil {
		return zero, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Match recovery/admission ordering. Authority is always an existing row:
	// a missing ledger fails, so concurrent initialization cannot bypass freeze.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return zero, err
	}
	var authority, raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return zero, err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", topology).Scan(&raw); err != nil {
		return zero, err
	}
	next, receipt, err := persistence.ApplyAuthorizedSnapshotRestore(raw, authority, topology, request)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(raw, next) {
		if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), topology); err != nil {
			return zero, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return receipt, nil
}
