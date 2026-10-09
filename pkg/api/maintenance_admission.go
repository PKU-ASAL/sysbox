package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Caller retains its write transaction through dispatch publication. A missing
// ledger is safe only while first barrier creation uses the same exclusion.
func checkSQLiteMaintenanceAdmission(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, uid string) error {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		raw = nil
	} else if err != nil {
		return err
	} else if len(raw) == 0 {
		return persistence.ErrCorruptRecord
	}
	return persistence.ValidateMaintenanceWrite(raw, uid)
}

func checkPostgresMaintenanceAdmission(ctx context.Context, tx pgx.Tx, uid string) error {
	if uid == "" {
		return persistence.ErrInvalidRecord
	}
	raw, err := lockPostgresExecutionAuthority(ctx, tx)
	if err != nil {
		return err
	}
	return persistence.ValidateMaintenanceWrite(raw, uid)
}

func lockPostgresExecutionAuthority(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	// Materialize an empty row before locking it; FOR UPDATE on an absent row
	// cannot serialize with the first concurrent BeginMaintenance.
	initial := &grantTransaction{}
	if err := persistence.NewExecutionGrantRepository(initial).ObserveTime(ctx); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES ('authority',$1::jsonb) ON CONFLICT(id) DO NOTHING", string(initial.raw)); err != nil {
		return nil, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}
