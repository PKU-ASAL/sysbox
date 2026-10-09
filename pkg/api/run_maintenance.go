package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"os"
	"path/filepath"
	"strings"
)

func validateRunAuthority(raw []byte, uid string) error {
	if uid == "" {
		return persistence.ErrInvalidRecord
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
		return nil
	}
	return persistence.ValidateMaintenanceWrite(raw, uid)
}

func checkLocalRunMaintenance(ctx context.Context, runsDir string, run controlplane.Run) error {
	raw, err := persistence.FilesystemExecutionGrantStorage(runsDir).ReadExecutionAuthority(ctx)
	if errors.Is(err, persistence.ErrNotFound) {
		raw = nil
	} else if err != nil {
		return err
	}
	lifecycle := filepath.Join(runsDir, run.Topology, "state.json.lifecycle")
	if _, err = os.Stat(lifecycle); errors.Is(err, os.ErrNotExist) {
		return validateRunAuthority(raw, "legacy")
	}
	repo, err := persistence.OpenFilesystem(lifecycle)
	if err != nil {
		return err
	}
	defer repo.Close()
	st, err := repo.LoadState(ctx, run.Topology)
	if err != nil {
		return err
	}
	return validateRunAuthority(raw, st.TopologyUID)
}

func checkSQLiteRunMaintenance(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, run controlplane.Run) error {
	authority, err := readSQLiteAuthority(ctx, conn)
	if err != nil {
		return err
	}
	var raw []byte
	err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", run.Topology).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err != nil && strings.Contains(err.Error(), "no such table") {
		return validateRunAuthority(authority, "legacy")
	}
	if err != nil {
		return err
	}
	var aggregate struct {
		State struct {
			TopologyUID string `json:"topology_uid"`
		} `json:"state"`
	}
	if err = json.Unmarshal(raw, &aggregate); err != nil {
		return persistence.ErrCorruptRecord
	}
	return validateRunAuthority(authority, aggregate.State.TopologyUID)
}

func readSQLiteAuthority(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) ([]byte, error) {
	var raw []byte
	err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

func checkPostgresRunMaintenance(ctx context.Context, tx pgx.Tx, authority []byte, run controlplane.Run) error {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", run.Topology).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return validateRunAuthority(authority, "legacy")
	}
	if err != nil {
		return err
	}
	var aggregate struct {
		State struct {
			TopologyUID string `json:"topology_uid"`
		} `json:"state"`
	}
	if err = json.Unmarshal(raw, &aggregate); err != nil {
		return persistence.ErrCorruptRecord
	}
	return validateRunAuthority(authority, aggregate.State.TopologyUID)
}
