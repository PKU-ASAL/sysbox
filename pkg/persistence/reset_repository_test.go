package persistence

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func resetTestRepository(t *testing.T, kind string) *Repository {
	t.Helper()
	var r *Repository
	var err error
	switch kind {
	case "filesystem":
		r, err = OpenFilesystem(t.TempDir())
	case "sqlite":
		r, err = OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	case "postgres":
		dsn := os.Getenv("SYSBOX_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("SYSBOX_TEST_POSTGRES_DSN not set")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		require.NoError(t, err)
		schema := pgx.Identifier{"reset_test_" + rand.Text()}.Sanitize()
		_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := conn.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE")
			require.NoError(t, err)
			require.NoError(t, conn.Close(cleanup))
		})
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		r, err = OpenPostgres(ctx, u.String())
		require.NoError(t, err)
	default:
		t.Fatalf("unknown backend %s", kind)
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	return r
}
