package controlstore

import (
	"bytes"
	"context"
	"database/sql/driver"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// commitOwnershipRetirement keeps authority and aggregate locked through the state
// and receipt write. It requires an already-persisted barrier; it cannot create
// authority, release a barrier, or replace the workflow's activity checks.
func (s *Transactions) CommitOwnershipRetirement(ctx context.Context, topology string, request controlplane.OwnershipTransferPlan) (controlplane.OwnershipRetirementProof, error) {
	var zero controlplane.OwnershipRetirementProof
	if err := ValidatePathSegment(topology, "topology"); err != nil {
		return zero, err
	}
	switch store := s.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return zero, err
		}
		defer unlock()
		repo, err := s.ActionRepository(topology)
		if err != nil {
			return zero, err
		}
		var receipt controlplane.OwnershipRetirementProof
		err = persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			var err error
			target, e := s.ActionRepository(request.TargetTopology)
			if e != nil {
				return nil, e
			}
			reserved, e := target.ReadOwnershipTarget(ctx, request.TargetTopology, request.ID)
			if e != nil || reserved.Aborted || reserved.DispositionHash != "" || GcEvidenceHash(reserved.Plan) != GcEvidenceHash(request) {
				return nil, persistence.ErrConflict
			}
			receipt, err = repo.RetireOwnership(ctx, request, authority)
			return nil, err // Keep the authority lock, but do not rewrite its ledger.
		})
		if err != nil {
			return zero, err
		}
		return receipt, nil
	case *SQLite:
		return commitSQLiteOwnershipRetirement(ctx, store, topology, request)
	case *Postgres:
		return commitPostgresOwnershipRetirement(ctx, store, topology, request)
	default:
		return zero, persistence.ErrUnsupportedVersion
	}
}

func commitSQLiteOwnershipRetirement(ctx context.Context, store *SQLite, topology string, request controlplane.OwnershipTransferPlan) (controlplane.OwnershipRetirementProof, error) {
	var zero controlplane.OwnershipRetirementProof
	db, err := store.OpenDB()
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
	var targetRaw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", request.TargetTopology).Scan(&targetRaw); err != nil {
		return zero, err
	}
	if err = persistence.ValidateOwnershipTargetRaw(targetRaw, request); err != nil {
		return zero, err
	}
	next, receipt, err := persistence.ApplyOwnershipRetirement(raw, authority, request)
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

func commitPostgresOwnershipRetirement(ctx context.Context, store *Postgres, topology string, request controlplane.OwnershipTransferPlan) (controlplane.OwnershipRetirementProof, error) {
	var zero controlplane.OwnershipRetirementProof
	pool, err := store.PoolFor(ctx)
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
	var targetRaw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", request.TargetTopology).Scan(&targetRaw); err != nil {
		return zero, err
	}
	if err = persistence.ValidateOwnershipTargetRaw(targetRaw, request); err != nil {
		return zero, err
	}
	next, receipt, err := persistence.ApplyOwnershipRetirement(raw, authority, request)
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
