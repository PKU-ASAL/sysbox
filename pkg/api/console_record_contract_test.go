package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleStoresPreserveCompleteRecordAndWorkspace(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		sess := controlplane.ConsoleSession{ID: "session", ProjectID: "project", Workspace: "workspace", Topology: "lab", AgentID: "agent", Node: "node", Status: controlplane.ConsoleSessionStatusQueued, CreatedAt: time.Now().UTC(), Roles: []string{"operator"}}
		require.NoError(t, store.SaveConsoleSession(ctx, sess))
		previous := sess
		sess.Status = controlplane.ConsoleSessionStatusRunning
		sess.StartedAt = time.Now().UTC()
		sess.Audit = []controlplane.Event{{Action: "allow", Message: "audit survived", CreatedAt: time.Now().UTC()}}
		require.NoError(t, store.CompareAndSwapConsoleSession(ctx, previous, sess))
		got, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		wantJSON, err := json.Marshal(sess)
		require.NoError(t, err)
		gotJSON, err := json.Marshal(got)
		require.NoError(t, err)
		require.JSONEq(t, string(wantJSON), string(gotJSON))
		items, err := store.ListConsoleSessions(ctx, "workspace")
		require.NoError(t, err)
		require.Len(t, items, 1)
		items, err = store.ListConsoleSessions(ctx, "lab")
		require.NoError(t, err)
		require.Empty(t, items, "workspace is not topology when explicitly set")
	})
}

func TestSQLiteConsoleRejectsMalformedFullRecord(t *testing.T) {
	store := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db")}
	db, err := store.open()
	require.NoError(t, err)
	defer db.Close()
	for _, raw := range []string{`null`, `{`, `{"id":"other"}`, `{"id":"session","audit":"invalid"}`, `{"id":"session","created_at":"not-time"}`} {
		_, err := db.Exec("INSERT INTO sysbox_console_sessions(id,data) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", "session", []byte(raw))
		require.NoError(t, err)
		_, err = store.GetConsoleSession(context.Background(), "session")
		require.ErrorIs(t, err, persistence.ErrCorruptRecord)
		items, err := store.ListConsoleSessions(context.Background(), "")
		require.ErrorIs(t, err, persistence.ErrCorruptRecord)
		require.Nil(t, items)
	}
}

func TestAPIConsoleColumnSchemaRequiresRebuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.db")
	store := &sqliteAPIStore{dbPath: path}
	db, err := store.open()
	require.NoError(t, err)
	_, err = db.Exec("UPDATE sysbox_api_schema_migrations SET version=9 WHERE name='api'")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = (&sqliteAPIStore{dbPath: path}).open()
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
}

func TestSQLiteConsoleSchemaValidatesFullRecordColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.db")
	store := &sqliteAPIStore{dbPath: path}
	db, err := store.open()
	require.NoError(t, err)
	_, err = db.Exec("ALTER TABLE sysbox_console_sessions RENAME COLUMN data TO obsolete_payload")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = (&sqliteAPIStore{dbPath: path}).open()
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
}
