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

func isOperationRecord(kind guestKind) bool {
	return kind == nodeOperationKind || kind == guestExecutionKind || kind == guestFileKind
}

func validateOperationMaintenance(kind guestKind, authority, old, next []byte, lifecycle func(string) ([]byte, error)) error {
	var topology string
	var draining bool
	switch kind {
	case nodeOperationKind:
		op, err := DecodeNodeOperation(next)
		if err != nil {
			return err
		}
		topology, draining = op.Topology, op.Status == controlplane.NodeOperationStatusDone || op.Status == controlplane.NodeOperationStatusFailed
	case guestExecutionKind:
		x, err := executionCodec.decode(next)
		if err != nil {
			return err
		}
		topology, draining = x.Topology, guestTerminal(x.Status)
	case guestFileKind:
		x, err := fileOperationCodec.decode(next)
		if err != nil {
			return err
		}
		topology, draining = x.Topology, guestTerminal(x.Status)
		if old != nil && x.CancelRequested && !draining {
			prior, err := fileOperationCodec.decode(old)
			if err != nil {
				return err
			}
			draining = x.Status == prior.Status
		}
	default:
		return ErrInvalidRecord
	}
	if old != nil && draining {
		return nil
	}
	if !topologyName.MatchString(topology) {
		return ErrInvalidRecord
	}
	raw, err := lifecycle(topology)
	if err != nil {
		return err
	}
	return ValidateRunMaintenance(authority, raw, topology)
}

func (b *fileGuestRecords) updateOperation(ctx context.Context, kind guestKind, id string, fn func([]byte) ([]byte, error)) error {
	return FilesystemExecutionGrantStorage(b.root).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
		return nil, b.updateRecord(ctx, kind, id, func(old []byte) ([]byte, error) {
			next, err := fn(old)
			if err != nil || next == nil {
				return next, err
			}
			err = validateOperationMaintenance(kind, authority, old, next, func(topology string) ([]byte, error) {
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

func checkSQLiteOperationMaintenance(ctx context.Context, conn *sql.Conn, kind guestKind, old, next []byte) error {
	var authority []byte
	err := conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority)
	if errors.Is(err, sql.ErrNoRows) {
		authority = nil
	} else if err != nil {
		return err
	} else if len(authority) == 0 {
		return ErrCorruptRecord
	}
	return validateOperationMaintenance(kind, authority, old, next, func(topology string) ([]byte, error) {
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

func lockOperationAuthority(ctx context.Context, tx pgx.Tx) ([]byte, error) {
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

func checkPostgresOperationMaintenance(ctx context.Context, tx pgx.Tx, kind guestKind, authority, old, next []byte) error {
	return validateOperationMaintenance(kind, authority, old, next, func(topology string) ([]byte, error) {
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
