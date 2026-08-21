package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *localAPIStore) LatestGuestAcceptance(_ context.Context, agentID string) (time.Time, time.Time, error) {
	executions, err := readLocalObjects[storedGuestExecution](filepath.Join(s.runsDir, "_guest-executions", "*.json"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	files, err := readLocalObjects[storedGuestFileOperation](filepath.Join(s.runsDir, "_guest-file-operations", "*.json"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var execAt, fileAt time.Time
	for _, item := range executions {
		if item.AgentID == agentID && item.Status == controlplane.GuestExecutionCompleted && item.ResultClass == controlplane.GuestExecutionResultClassExit && item.Err == "" && item.EndedAt.After(execAt) {
			execAt = item.EndedAt
		}
	}
	for _, item := range files {
		if item.AgentID == agentID && item.Status == controlplane.GuestExecutionCompleted && item.Error == "" && item.EndedAt.After(fileAt) {
			fileAt = item.EndedAt
		}
	}
	return execAt, fileAt, nil
}

func (s *postgresAPIStore) LatestGuestAcceptance(ctx context.Context, agentID string) (time.Time, time.Time, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	defer conn.Close(ctx)
	var execAt, fileAt *time.Time
	err = conn.QueryRow(ctx, `
SELECT
  (SELECT max((data->>'ended_at')::timestamptz) FROM sysbox_guest_executions WHERE data->>'agent_id'=$1 AND data->>'status'='completed' AND data->>'result_class'='exit' AND coalesce(data->>'error','')=''),
  (SELECT max((data->>'ended_at')::timestamptz) FROM sysbox_guest_file_operations WHERE data->>'agent_id'=$1 AND data->>'status'='completed' AND coalesce(data->>'error','')='')`, agentID).Scan(&execAt, &fileAt)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var execValue, fileValue time.Time
	if execAt != nil {
		execValue = execAt.UTC()
	}
	if fileAt != nil {
		fileValue = fileAt.UTC()
	}
	return execValue, fileValue, nil
}

func (s *sqliteAPIStore) LatestGuestAcceptance(ctx context.Context, agentID string) (time.Time, time.Time, error) {
	db, err := s.open()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var execAt, fileAt time.Time
	rows, err := db.QueryContext(ctx, `SELECT data FROM sysbox_guest_executions`)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return time.Time{}, time.Time{}, err
		}
		item, err := decodeStoredGuestExecution(raw)
		if err != nil {
			rows.Close()
			return time.Time{}, time.Time{}, err
		}
		if item.AgentID == agentID && item.Status == controlplane.GuestExecutionCompleted && item.ResultClass == controlplane.GuestExecutionResultClassExit && item.Err == "" && item.EndedAt.After(execAt) {
			execAt = item.EndedAt
		}
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, time.Time{}, err
	}
	rows, err = db.QueryContext(ctx, `SELECT data FROM sysbox_guest_file_operations`)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return time.Time{}, time.Time{}, err
		}
		item, err := decodeGuestFileOperation(raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if item.AgentID == agentID && item.Status == controlplane.GuestExecutionCompleted && item.Error == "" && item.EndedAt.After(fileAt) {
			fileAt = item.EndedAt
		}
	}
	return execAt.UTC(), fileAt.UTC(), rows.Err()
}

var guestExecutionCASMu sync.Mutex

type storedGuestExecution struct {
	controlplane.GuestExecution
	AgentID   string                             `json:"agent_id"`
	CommandID string                             `json:"command_id,omitempty"`
	Request   controlplane.GuestExecutionRequest `json:"request"`
}

func encodeStoredGuestExecution(execution controlplane.GuestExecution) ([]byte, error) {
	return json.Marshal(storedGuestExecution{GuestExecution: execution, AgentID: execution.AgentID, CommandID: execution.CommandID, Request: execution.Request})
}

func decodeStoredGuestExecution(raw []byte) (*controlplane.GuestExecution, error) {
	var stored storedGuestExecution
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	stored.GuestExecution.Request = stored.Request
	stored.GuestExecution.AgentID = stored.AgentID
	stored.GuestExecution.CommandID = stored.CommandID
	return &stored.GuestExecution, nil
}

func (s *localAPIStore) SaveGuestExecution(_ context.Context, execution controlplane.GuestExecution) error {
	path := filepath.Join(s.runsDir, "_guest-executions", execution.ID+".json")
	if err := writeLocalObject(path, storedGuestExecution{GuestExecution: execution, AgentID: execution.AgentID, CommandID: execution.CommandID, Request: execution.Request}); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s *localAPIStore) GetGuestExecution(_ context.Context, id string) (*controlplane.GuestExecution, error) {
	items, err := readLocalObjects[storedGuestExecution](filepath.Join(s.runsDir, "_guest-executions", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			item.GuestExecution.AgentID = item.AgentID
			item.GuestExecution.CommandID = item.CommandID
			item.GuestExecution.Request = item.Request
			return &item.GuestExecution, nil
		}
	}
	return nil, fmt.Errorf("guest execution not found")
}

func (s *localAPIStore) CompareAndSwapGuestExecution(ctx context.Context, execution controlplane.GuestExecution, expected int64) (bool, error) {
	guestExecutionCASMu.Lock()
	defer guestExecutionCASMu.Unlock()
	current, err := s.GetGuestExecution(ctx, execution.ID)
	if err != nil {
		return false, err
	}
	if current.Version != expected {
		return false, nil
	}
	execution.Version = expected + 1
	return true, s.SaveGuestExecution(ctx, execution)
}

func (s *postgresAPIStore) SaveGuestExecution(ctx context.Context, execution controlplane.GuestExecution) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := encodeStoredGuestExecution(execution)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `INSERT INTO sysbox_guest_executions (id, data, updated_at) VALUES ($1,$2::jsonb,now()) ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`, execution.ID, string(raw))
	return err
}

func (s *postgresAPIStore) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	if err := conn.QueryRow(ctx, `SELECT data::text FROM sysbox_guest_executions WHERE id=$1`, id).Scan(&raw); err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("guest execution not found")
		}
		return nil, err
	}
	return decodeStoredGuestExecution(raw)
}

func (s *postgresAPIStore) CompareAndSwapGuestExecution(ctx context.Context, execution controlplane.GuestExecution, expected int64) (bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)
	execution.Version = expected + 1
	raw, err := encodeStoredGuestExecution(execution)
	if err != nil {
		return false, err
	}
	result, err := conn.Exec(ctx, `UPDATE sysbox_guest_executions SET data=$1::jsonb, updated_at=now() WHERE id=$2 AND (data->>'version')::bigint=$3`, string(raw), execution.ID, expected)
	return result.RowsAffected() == 1, err
}

func (s *sqliteAPIStore) SaveGuestExecution(ctx context.Context, execution controlplane.GuestExecution) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	raw, err := encodeStoredGuestExecution(execution)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO sysbox_guest_executions (id,data) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, execution.ID, raw)
	return err
}

func (s *sqliteAPIStore) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT data FROM sysbox_guest_executions WHERE id=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("guest execution not found")
		}
		return nil, err
	}
	return decodeStoredGuestExecution(raw)
}

func (s *sqliteAPIStore) CompareAndSwapGuestExecution(ctx context.Context, execution controlplane.GuestExecution, expected int64) (bool, error) {
	db, err := s.open()
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var currentRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT data FROM sysbox_guest_executions WHERE id=?`, execution.ID).Scan(&currentRaw); err != nil {
		return false, err
	}
	current, err := decodeStoredGuestExecution(currentRaw)
	if err != nil {
		return false, err
	}
	if current.Version != expected {
		return false, nil
	}
	execution.Version = expected + 1
	raw, err := encodeStoredGuestExecution(execution)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sysbox_guest_executions SET data=? WHERE id=?`, raw, execution.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
