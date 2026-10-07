package api

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestSQLiteSchemaRejectsUnsupportedVersionWithoutMutation(t *testing.T) {
	for _, version := range []int{6, 99} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "api.db")
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer db.Close()
			_, err = db.Exec(`CREATE TABLE sysbox_api_schema_migrations(name TEXT PRIMARY KEY,version INTEGER NOT NULL); INSERT INTO sysbox_api_schema_migrations VALUES('api',?)`, version)
			require.NoError(t, err)
			_, err = (&sqliteAPIStore{dbPath: path}).open()
			require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
			var count, actual int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, db.QueryRow(`SELECT version FROM sysbox_api_schema_migrations`).Scan(&actual))
			require.Equal(t, version, actual)
		})
	}
}

func TestSQLiteSchemaConcurrentFreshOpenAndStrictReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.db")
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := (&sqliteAPIStore{dbPath: path}).open()
			if err == nil {
				db.Close()
			}
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	s := &sqliteAPIStore{dbPath: path}
	db, err := s.open()
	require.NoError(t, err)
	defer db.Close()
	version, err := s.SchemaVersion(context.Background())
	require.NoError(t, err)
	require.Equal(t, apiSchemaVersion, version)
	_, err = db.Exec(`DROP TABLE sysbox_projection`)
	require.NoError(t, err)
	_, err = (&sqliteAPIStore{dbPath: path}).open()
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='sysbox_projection'`).Scan(&count))
	require.Zero(t, count)
}
