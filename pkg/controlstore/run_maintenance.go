package controlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func checkLocalRunMaintenance(ctx context.Context, runsDir string, authority []byte, run controlplane.Run) error {
	if run.Status.IsTerminal() {
		return nil
	}
	return checkLocalTopologyMaintenance(ctx, runsDir, authority, run.Topology, run.AgentID)
}

func checkLocalTopologyMaintenance(ctx context.Context, runsDir string, authority []byte, topology string, agents ...string) error {
	if err := ValidatePathSegment(topology, "topology"); err != nil {
		return err
	}
	// The caller holds authority through Run append. No lifecycle is created by
	// checking admission; atomic rename supplies a complete aggregate observation.
	raw, err := os.ReadFile(filepath.Join(runsDir, topology, "state.json.lifecycle", topology+".json"))
	if errors.Is(err, os.ErrNotExist) {
		raw = nil
	} else if err != nil {
		return err
	}
	return persistence.ValidateRunMaintenance(authority, raw, topology, agents...)
}

func checkSQLiteRunMaintenance(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, run controlplane.Run) error {
	if run.Status.IsTerminal() {
		return nil
	}
	return checkSQLiteTopologyMaintenance(ctx, conn, run.Topology, run.AgentID)
}

func checkSQLiteTopologyMaintenance(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, topology string, agents ...string) error {
	authority, err := readSQLiteAuthority(ctx, conn)
	if err != nil {
		return err
	}
	var exists int
	if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sysbox_lifecycle_aggregates'").Scan(&exists); err != nil {
		return err
	}
	var raw []byte
	if exists != 0 {
		err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			raw = nil
		} else if err != nil {
			return err
		} else if len(raw) == 0 {
			return persistence.ErrCorruptRecord
		}
	}
	return persistence.ValidateRunMaintenance(authority, raw, topology, agents...)
}

func readSQLiteAuthority(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) ([]byte, error) {
	var raw []byte
	err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err == nil && len(raw) == 0 {
		return nil, persistence.ErrCorruptRecord
	}
	return raw, err
}

func checkPostgresRunMaintenance(ctx context.Context, tx pgx.Tx, authority []byte, run controlplane.Run) error {
	if run.Status.IsTerminal() {
		return nil
	}
	return checkPostgresTopologyMaintenance(ctx, tx, authority, run.Topology, run.AgentID)
}

func checkPostgresTopologyMaintenance(ctx context.Context, tx pgx.Tx, authority []byte, topology string, agents ...string) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('sysbox_lifecycle_aggregates') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	var raw []byte
	if exists {
		err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1", topology).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			raw = nil
		} else if err != nil {
			return err
		}
	}
	return persistence.ValidateRunMaintenance(authority, raw, topology, agents...)
}
