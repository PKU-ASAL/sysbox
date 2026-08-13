package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
)

var guestFileOperationCASMu sync.Mutex
var errGuestFileOperationStoreNotFound = errors.New("guest file operation not found")

type storedGuestFileOperation struct {
	controlplane.GuestFileOperation
	AgentID         string `json:"agent_id"`
	CommandID       string `json:"command_id,omitempty"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
}

func encodeGuestFileOperation(op controlplane.GuestFileOperation) ([]byte, error) {
	return json.Marshal(storedGuestFileOperation{GuestFileOperation: op, AgentID: op.AgentID, CommandID: op.CommandID, CancelRequested: op.CancelRequested})
}
func decodeGuestFileOperation(raw []byte) (*controlplane.GuestFileOperation, error) {
	var stored storedGuestFileOperation
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	stored.GuestFileOperation.AgentID, stored.GuestFileOperation.CommandID = stored.AgentID, stored.CommandID
	stored.GuestFileOperation.CancelRequested = stored.CancelRequested
	return &stored.GuestFileOperation, nil
}

func (s *localAPIStore) SaveGuestFileOperation(_ context.Context, op controlplane.GuestFileOperation) error {
	path := filepath.Join(s.runsDir, "_guest-file-operations", op.ID+".json")
	if err := writeLocalObject(path, storedGuestFileOperation{GuestFileOperation: op, AgentID: op.AgentID, CommandID: op.CommandID, CancelRequested: op.CancelRequested}); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s *localAPIStore) GetGuestFileOperation(_ context.Context, id string) (*controlplane.GuestFileOperation, error) {
	items, err := readLocalObjects[storedGuestFileOperation](filepath.Join(s.runsDir, "_guest-file-operations", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, stored := range items {
		if stored.ID == id {
			stored.GuestFileOperation.AgentID, stored.GuestFileOperation.CommandID = stored.AgentID, stored.CommandID
			stored.GuestFileOperation.CancelRequested = stored.CancelRequested
			return &stored.GuestFileOperation, nil
		}
	}
	return nil, errGuestFileOperationStoreNotFound
}

func (s *localAPIStore) CompareAndSwapGuestFileOperation(ctx context.Context, op controlplane.GuestFileOperation, expected int64) (bool, error) {
	guestFileOperationCASMu.Lock()
	defer guestFileOperationCASMu.Unlock()
	current, err := s.GetGuestFileOperation(ctx, op.ID)
	if err != nil {
		return false, err
	}
	if current.Version != expected {
		return false, nil
	}
	op.Version = expected + 1
	return true, s.SaveGuestFileOperation(ctx, op)
}

func (s *postgresAPIStore) SaveGuestFileOperation(ctx context.Context, op controlplane.GuestFileOperation) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := encodeGuestFileOperation(op)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `INSERT INTO sysbox_guest_file_operations (id,data,updated_at) VALUES ($1,$2::jsonb,now()) ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data,updated_at=now()`, op.ID, string(raw))
	return err
}

func (s *postgresAPIStore) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	if err := conn.QueryRow(ctx, `SELECT data::text FROM sysbox_guest_file_operations WHERE id=$1`, id).Scan(&raw); err != nil {
		if err == pgx.ErrNoRows {
			return nil, errGuestFileOperationStoreNotFound
		}
		return nil, err
	}
	return decodeGuestFileOperation(raw)
}

func (s *postgresAPIStore) CompareAndSwapGuestFileOperation(ctx context.Context, op controlplane.GuestFileOperation, expected int64) (bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)
	op.Version = expected + 1
	raw, err := encodeGuestFileOperation(op)
	if err != nil {
		return false, err
	}
	tag, err := conn.Exec(ctx, `UPDATE sysbox_guest_file_operations SET data=$1::jsonb,updated_at=now() WHERE id=$2 AND (data->>'version')::bigint=$3`, string(raw), op.ID, expected)
	return tag.RowsAffected() == 1, err
}

func (s *sqliteAPIStore) SaveGuestFileOperation(ctx context.Context, op controlplane.GuestFileOperation) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	raw, err := encodeGuestFileOperation(op)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO sysbox_guest_file_operations (id,data) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, op.ID, raw)
	return err
}

func (s *sqliteAPIStore) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT data FROM sysbox_guest_file_operations WHERE id=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, errGuestFileOperationStoreNotFound
		}
		return nil, err
	}
	return decodeGuestFileOperation(raw)
}

func (s *sqliteAPIStore) CompareAndSwapGuestFileOperation(ctx context.Context, op controlplane.GuestFileOperation, expected int64) (bool, error) {
	db, err := s.open()
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT data FROM sysbox_guest_file_operations WHERE id=?`, op.ID).Scan(&raw); err != nil {
		return false, err
	}
	current, err := decodeGuestFileOperation(raw)
	if err != nil {
		return false, err
	}
	if current.Version != expected {
		return false, nil
	}
	op.Version = expected + 1
	raw, err = encodeGuestFileOperation(op)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sysbox_guest_file_operations SET data=? WHERE id=?`, raw, op.ID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
