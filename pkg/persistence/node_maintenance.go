package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func NodeOperationUnresolved(op controlplane.NodeOperation) bool {
	return op.CreatedAt.IsZero() || op.EndedAt.IsZero() || op.EndedAt.Before(op.CreatedAt) || (op.Status != controlplane.NodeOperationStatusDone && op.Status != controlplane.NodeOperationStatusFailed)
}

func validateNodeMaintenance(authority, old, next []byte, lifecycle func(string) ([]byte, error)) error {
	op, err := DecodeNodeOperation(next)
	if err != nil {
		return err
	}
	if old != nil && (op.Status == controlplane.NodeOperationStatusDone || op.Status == controlplane.NodeOperationStatusFailed) {
		return nil
	}
	if !topologyName.MatchString(op.Topology) {
		return ErrInvalidRecord
	}
	raw, err := lifecycle(op.Topology)
	if err != nil {
		return err
	}
	return ValidateRunMaintenance(authority, raw, op.Topology)
}

func (b *fileGuestRecords) updateNode(ctx context.Context, id string, fn func([]byte) ([]byte, error)) error {
	return FilesystemExecutionGrantStorage(b.root).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
		return nil, b.updateRecord(ctx, nodeOperationKind, id, func(old []byte) ([]byte, error) {
			next, err := fn(old)
			if err != nil || next == nil {
				return next, err
			}
			err = validateNodeMaintenance(authority, old, next, func(topology string) ([]byte, error) {
				raw, err := os.ReadFile(filepath.Join(b.root, topology, "state.json.lifecycle", topology+".json"))
				if errors.Is(err, os.ErrNotExist) {
					return nil, nil
				}
				return raw, err
			})
			return next, err
		})
	})
}

func checkSQLiteNodeMaintenance(ctx context.Context, conn *sql.Conn, old, next []byte) error {
	var authority []byte
	err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority)
	if errors.Is(err, sql.ErrNoRows) {
		authority = nil
	} else if err != nil {
		return err
	} else if len(authority) == 0 {
		return ErrCorruptRecord
	}
	return validateNodeMaintenance(authority, old, next, func(topology string) ([]byte, error) {
		var exists int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sysbox_lifecycle_aggregates'").Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, nil
		}
		var raw []byte
		err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err == nil && len(raw) == 0 {
			return nil, ErrCorruptRecord
		}
		return raw, err
	})
}

func lockNodeAuthority(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	initial, err := decodeExecutionAuthority(nil)
	if err != nil {
		return nil, err
	}
	initial.ObservedAt = time.Now().UTC()
	raw, err := json.Marshal(initial)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sysbox_execution_authority(id,data) VALUES('authority',$1::jsonb) ON CONFLICT(id) DO NOTHING", string(raw)); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&raw)
	return raw, err
}

func checkPostgresNodeMaintenance(ctx context.Context, tx pgx.Tx, authority, old, next []byte) error {
	return validateNodeMaintenance(authority, old, next, func(topology string) ([]byte, error) {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass('sysbox_lifecycle_aggregates') IS NOT NULL").Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, nil
		}
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1", topology).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return raw, err
	})
}
