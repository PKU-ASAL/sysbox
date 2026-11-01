package controlstore

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func checkSQLiteRunAdmission(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, run controlplane.Run) error {
	if !run.Status.IsActive() {
		return nil
	}
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sysbox_runs WHERE topology=? AND id<>? AND status IN ('queued','assigned','running') AND rowid IN (SELECT max(rowid) FROM sysbox_runs GROUP BY id)`, run.Topology, run.ID).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrActiveOperation
	}
	return nil
}

func checkPostgresRunAdmission(ctx context.Context, tx pgx.Tx, run controlplane.Run) error {
	if !run.Status.IsActive() {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sysbox_runs WHERE topology=$1 AND id<>$2 AND status IN ('queued','assigned','running')`, run.Topology, run.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrActiveOperation
	}
	return nil
}
