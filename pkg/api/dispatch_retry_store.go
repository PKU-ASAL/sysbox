package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/persistence"
	"os"
	"path/filepath"
)

func (s *localAPIStore) retryPath(id string) string {
	return filepath.Join(s.runsDir, "_dispatch-retries", id+".json")
}
func (s *localAPIStore) LoadDispatchRetry(ctx context.Context, id string) (DispatchRetryRecord, error) {
	var r DispatchRetryRecord
	raw, err := os.ReadFile(s.retryPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return r, persistence.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (s *localAPIStore) SaveDispatchRetry(ctx context.Context, r DispatchRetryRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeLocalObject(s.retryPath(r.CommandID), json.RawMessage(raw))
}
func (s *localAPIStore) ClearDispatchRetry(ctx context.Context, id string) error {
	err := os.Remove(s.retryPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s *sqliteAPIStore) LoadDispatchRetry(ctx context.Context, id string) (DispatchRetryRecord, error) {
	db, err := s.open()
	if err != nil {
		return DispatchRetryRecord{}, err
	}
	var r DispatchRetryRecord
	var next string
	err = db.QueryRowContext(ctx, "SELECT command_id,failures,next,last_error FROM sysbox_dispatch_retries WHERE command_id=?", id).Scan(&r.CommandID, &r.Failures, &next, &r.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return r, persistence.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Next = parseSQLiteTime(next)
	return r, nil
}
func (s *sqliteAPIStore) SaveDispatchRetry(ctx context.Context, r DispatchRetryRecord) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO sysbox_dispatch_retries(command_id,failures,next,last_error) VALUES(?,?,?,?) ON CONFLICT(command_id) DO UPDATE SET failures=excluded.failures,next=excluded.next,last_error=excluded.last_error", r.CommandID, r.Failures, formatSQLiteTime(r.Next), r.LastError)
	return err
}
func (s *sqliteAPIStore) ClearDispatchRetry(ctx context.Context, id string) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "DELETE FROM sysbox_dispatch_retries WHERE command_id=?", id)
	return err
}
func (s *postgresAPIStore) LoadDispatchRetry(ctx context.Context, id string) (DispatchRetryRecord, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return DispatchRetryRecord{}, err
	}
	defer conn.Release()
	var r DispatchRetryRecord
	err = conn.QueryRow(ctx, "SELECT command_id,failures,next,last_error FROM sysbox_dispatch_retries WHERE command_id=$1", id).Scan(&r.CommandID, &r.Failures, &r.Next, &r.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, persistence.ErrNotFound
	}
	return r, err
}
func (s *postgresAPIStore) SaveDispatchRetry(ctx context.Context, r DispatchRetryRecord) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, "INSERT INTO sysbox_dispatch_retries(command_id,failures,next,last_error) VALUES($1,$2,$3,$4) ON CONFLICT(command_id) DO UPDATE SET failures=EXCLUDED.failures,next=EXCLUDED.next,last_error=EXCLUDED.last_error", r.CommandID, r.Failures, r.Next, r.LastError)
	return err
}
func (s *postgresAPIStore) ClearDispatchRetry(ctx context.Context, id string) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, "DELETE FROM sysbox_dispatch_retries WHERE command_id=$1", id)
	return err
}
