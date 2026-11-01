package controlstore

import (
	"bytes"
	"context"
	"database/sql/driver"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func claimSQLiteGCExecution(ctx context.Context, store *SQLite, topology string, candidate controlplane.GCCandidate, grant controlplane.ExecutionGrant) error {

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
	var authority, raw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&raw); err != nil {
		return err
	}
	next, err := persistence.ApplyGCExecutionClaim(raw, authority, candidate, grant)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, next) {
		if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, topology); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func claimPostgresGCExecution(ctx context.Context, store *Postgres, topology string, candidate controlplane.GCCandidate, grant controlplane.ExecutionGrant) error {

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
	// Match recovery/admission ordering. Authority is always an existing row:
	// a missing ledger fails, so concurrent initialization cannot bypass freeze.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return err
	}
	var authority, raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", topology).Scan(&raw); err != nil {
		return err
	}
	next, err := persistence.ApplyGCExecutionClaim(raw, authority, candidate, grant)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, next) {
		if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), topology); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Transactions) ClaimGCExecution(ctx context.Context, topology string, c controlplane.GCCandidate, g controlplane.ExecutionGrant) error {
	switch store := s.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		return persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			repo, err := s.ActionRepository(topology)
			if err != nil {
				return nil, err
			}
			return nil, repo.ClaimGCExecutionWithAuthority(ctx, topology, c, g, authority)
		})
	case *SQLite:
		return claimSQLiteGCExecution(ctx, store, topology, c, g)
	case *Postgres:
		return claimPostgresGCExecution(ctx, store, topology, c, g)
	default:
		return persistence.ErrUnsupportedVersion
	}
}
