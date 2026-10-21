package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func beginSQLiteOwnershipMaintenance(ctx context.Context, store *sqliteAPIStore, topology string, request controlplane.OwnershipTransferPlan) (*persistence.MaintenanceBarrier, error) {
	var zero *persistence.MaintenanceBarrier
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
	var targetRaw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", request.TargetTopology).Scan(&targetRaw); err != nil {
		return zero, err
	}
	if err = persistence.ValidateOwnershipTargetRaw(targetRaw, request); err != nil {
		return zero, err
	}
	next, receipt, err := beginOwnershipAuthority(ctx, authority, request)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(authority, next) {
		if _, err = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", next); err != nil {
			return zero, err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return zero, err
	}
	committed = true
	return receipt, nil
}

func beginPostgresOwnershipMaintenance(ctx context.Context, store *postgresAPIStore, topology string, request controlplane.OwnershipTransferPlan) (*persistence.MaintenanceBarrier, error) {
	var zero *persistence.MaintenanceBarrier
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
	var targetRaw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", request.TargetTopology).Scan(&targetRaw); err != nil {
		return zero, err
	}
	if err = persistence.ValidateOwnershipTargetRaw(targetRaw, request); err != nil {
		return zero, err
	}
	next, receipt, err := beginOwnershipAuthority(ctx, authority, request)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(authority, next) {
		if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb WHERE id='authority'", string(next)); err != nil {
			return zero, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return receipt, nil
}

func beginOwnershipAuthority(ctx context.Context, raw []byte, p controlplane.OwnershipTransferPlan) ([]byte, *persistence.MaintenanceBarrier, error) {
	memory := &grantTransaction{raw: raw}
	digest, err := p.Digest()
	if err != nil {
		return nil, nil, err
	}
	barrier, err := persistence.NewExecutionGrantRepository(memory).BeginMaintenance(ctx, p.ID, p.Source.TopologyUID, digest)
	return memory.raw, barrier, err
}
func (s *Server) beginOwnershipMaintenance(ctx context.Context, p controlplane.OwnershipTransferPlan) (*persistence.MaintenanceBarrier, error) {
	if err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).ObserveTime(ctx); err != nil {
		return nil, err
	}
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		release, err := store.lockRunRecords(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		var barrier *persistence.MaintenanceBarrier
		err = persistence.FilesystemExecutionGrantStorage(store.runsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			target, err := s.actionRepository(p.TargetTopology)
			if err != nil {
				return nil, err
			}
			v, err := target.ReadOwnershipTarget(ctx, p.TargetTopology, p.ID)
			if err != nil || v.Aborted || gcEvidenceHash(v.Plan) != gcEvidenceHash(p) {
				return nil, persistence.ErrConflict
			}
			next, b, err := beginOwnershipAuthority(ctx, raw, p)
			barrier = b
			return next, err
		})
		return barrier, err
	case *sqliteAPIStore:
		return beginSQLiteOwnershipMaintenance(ctx, store, p.SourceTopology, p)
	case *postgresAPIStore:
		return beginPostgresOwnershipMaintenance(ctx, store, p.SourceTopology, p)
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
}
