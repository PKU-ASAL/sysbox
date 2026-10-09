package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	if err := persistence.ValidateConsoleSessionCAS(old, next); err != nil {
		return err
	}
	raw, err := persistence.EncodeConsoleSession(next)
	if err != nil {
		return err
	}
	unlock, err := s.lockRunRecords(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := s.GetConsoleSession(ctx, old.ID)
	if errors.Is(err, errConsoleSessionNotFound) {
		return persistence.ErrConflict
	}
	if err != nil {
		return err
	}
	if !persistence.SameConsoleSession(*current, old) {
		return persistence.ErrConflict
	}
	workspace := next.Workspace
	if workspace == "" {
		workspace = next.Topology
	}
	return writeLocalObject(filepath.Join(s.runsDir, workspace, "sessions", next.ID+".json"), json.RawMessage(raw))
}

func (s *sqliteAPIStore) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	if err := persistence.ValidateConsoleSessionCAS(old, next); err != nil {
		return err
	}
	before, err := persistence.EncodeConsoleSession(old)
	if err != nil {
		return err
	}
	after, err := persistence.EncodeConsoleSession(next)
	if err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, "UPDATE sysbox_console_sessions SET data=? WHERE id=? AND data=?", after, old.ID, before)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return persistence.ErrConflict
	}
	return nil
}

func (s *postgresAPIStore) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	if err := persistence.ValidateConsoleSessionCAS(old, next); err != nil {
		return err
	}
	before, err := persistence.EncodeConsoleSession(old)
	if err != nil {
		return err
	}
	after, err := persistence.EncodeConsoleSession(next)
	if err != nil {
		return err
	}
	pool, err := s.poolFor(ctx)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, "UPDATE sysbox_console_sessions SET data=$1::jsonb,updated_at=now() WHERE id=$2 AND data=$3::jsonb", string(after), old.ID, string(before))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return persistence.ErrConflict
	}
	return nil
}
