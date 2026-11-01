package controlstore

import (
	"bytes"
	"context"
	"database/sql/driver"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func abortSQLiteOwnershipTransfer(ctx context.Context, store *SQLite, topology string, request controlplane.OwnershipTransferPlan) error {

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
	var targetRaw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", request.TargetTopology).Scan(&targetRaw); err != nil {
		return err
	}
	next, err := persistence.ApplyOwnershipAbort(raw, targetRaw, authority, request)
	if err != nil {
		return err
	}
	if !bytes.Equal(targetRaw, next) {
		if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, request.TargetTopology); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func abortPostgresOwnershipTransfer(ctx context.Context, store *Postgres, topology string, request controlplane.OwnershipTransferPlan) error {

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
	var targetRaw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", request.TargetTopology).Scan(&targetRaw); err != nil {
		return err
	}
	next, err := persistence.ApplyOwnershipAbort(raw, targetRaw, authority, request)
	if err != nil {
		return err
	}
	if !bytes.Equal(targetRaw, next) {
		if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), request.TargetTopology); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Transactions) AbortOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := persistence.NewExecutionGrantRepository(AuthorityStorage{Store: s.Store}).ObserveTime(ctx); err != nil {
		return err
	}
	switch store := s.Store.(type) {
	case *Local:
		release, err := store.LockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer release()
		return persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			if err := persistence.ValidateOwnershipAbortAuthority(authority, p); err != nil {
				return nil, err
			}
			source, err := s.ActionRepository(p.SourceTopology)
			if err != nil {
				return nil, err
			}
			retired, err := source.HasOwnershipRetirement(ctx, p.SourceTopology, p.ID)
			if err != nil {
				return nil, err
			}
			if retired {
				return nil, persistence.ErrConflict
			}
			target, err := s.ActionRepository(p.TargetTopology)
			if err != nil {
				return nil, err
			}
			return nil, target.AbortOwnershipTarget(ctx, p)
		})
	case *SQLite:
		return abortSQLiteOwnershipTransfer(ctx, store, p.SourceTopology, p)
	case *Postgres:
		return abortPostgresOwnershipTransfer(ctx, store, p.SourceTopology, p)
	default:
		return persistence.ErrUnsupportedVersion
	}
}
