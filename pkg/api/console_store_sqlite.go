package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *sqliteAPIStore) SaveConsoleSession(ctx context.Context, sess controlplane.ConsoleSession) error {
	if err := validatePathSegment(sess.ID, "session"); err != nil {
		return err
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO sysbox_console_sessions(id,data) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", sess.ID, raw)
	return err
}

func decodeSQLiteConsole(id string, raw []byte) (*controlplane.ConsoleSession, error) {
	var sess *controlplane.ConsoleSession
	if json.Unmarshal(raw, &sess) != nil || sess == nil || sess.ID != id || validatePathSegment(id, "session") != nil {
		return nil, persistence.ErrCorruptRecord
	}
	return sess, nil
}

func (s *sqliteAPIStore) GetConsoleSession(ctx context.Context, id string) (*controlplane.ConsoleSession, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = db.QueryRowContext(ctx, "SELECT data FROM sysbox_console_sessions WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errConsoleSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeSQLiteConsole(id, raw)
}

func (s *sqliteAPIStore) ListConsoleSessions(ctx context.Context, workspace string) ([]controlplane.ConsoleSession, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id,data FROM sysbox_console_sessions ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.ConsoleSession
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		sess, err := decodeSQLiteConsole(id, raw)
		if err != nil {
			return nil, err
		}
		w := sess.Workspace
		if w == "" {
			w = sess.Topology
		}
		if workspace == "" || workspace == w {
			out = append(out, *sess)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
