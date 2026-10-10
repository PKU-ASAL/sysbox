package persistence

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestExecutionLockProcess(t *testing.T) {
	if root := os.Getenv("SYSBOX_EXECUTION_LOCK_TEST_ROOT"); root != "" {
		var r *Repository
		var err error
		if os.Getenv("SYSBOX_EXECUTION_LOCK_TEST_BACKEND") == "sqlite" {
			r, err = OpenSQLite(filepath.Join(root, "state.db"))
		} else {
			r, err = OpenFilesystem(root)
		}
		require.NoError(t, err)
		defer r.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		unlock, err := r.LockExecution(ctx, "lab")
		if os.Getenv("SYSBOX_EXECUTION_LOCK_TEST_BLOCKED") == "true" {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.NoError(t, err)
			unlock()
		}
		return
	}
	for _, kind := range []string{"filesystem", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			var r *Repository
			var err error
			if kind == "sqlite" {
				r, err = OpenSQLite(filepath.Join(root, "state.db"))
			} else {
				r, err = OpenFilesystem(root)
			}
			require.NoError(t, err)
			defer r.Close()
			unlock, err := r.LockExecution(context.Background(), "lab")
			require.NoError(t, err)
			defer unlock()
			probe := func(blocked string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecutionLockProcess$")
				cmd.Env = append(os.Environ(), "SYSBOX_EXECUTION_LOCK_TEST_ROOT="+root, "SYSBOX_EXECUTION_LOCK_TEST_BACKEND="+kind, "SYSBOX_EXECUTION_LOCK_TEST_BLOCKED="+blocked)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
			probe("true")
			unlock()
			probe("false")
		})
	}
}

func TestExecutionLockSeparatesExecutionFromTransactions(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			ctx := context.Background()
			unlock, err := r.LockExecution(ctx, "lab")
			require.NoError(t, err)
			t.Cleanup(unlock)
			waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			_, err = r.LockExecution(waiting, "lab")
			require.ErrorIs(t, err, context.DeadlineExceeded)
			other, err := r.LockExecution(ctx, "other")
			require.NoError(t, err)
			other()
			transaction, cancelTransaction := context.WithTimeout(ctx, time.Second)
			defer cancelTransaction()
			_, err = r.LoadState(transaction, "lab")
			require.NoError(t, err, "execution lock must not hold aggregate transaction lock")
			unlock()
			again, err := r.LockExecution(transaction, "lab")
			require.NoError(t, err)
			again()
			_, err = r.LockExecution(ctx, "../escape")
			require.Error(t, err)
			canceled, stop := context.WithCancel(ctx)
			stop()
			_, err = r.LockExecution(canceled, "lab")
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestSQLiteExecutionLockUsesDatabaseIdentityThroughSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	r, err := OpenSQLite(path)
	require.NoError(t, err)
	defer r.Close()
	alias := filepath.Join(root, "alias.db")
	require.NoError(t, os.Symlink(path, alias))
	other, err := OpenSQLite(alias)
	require.NoError(t, err)
	defer other.Close()
	unlock, err := r.LockExecution(context.Background(), "lab")
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	second, err := other.LockExecution(ctx, "lab")
	if second != nil {
		defer second()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestPostgresExecutionLockUsesResolvedTableIdentity(t *testing.T) {
	r := resetTestRepository(t, "postgres")
	b := r.backend.(*sqlBackend)
	u, err := url.Parse(b.dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", "pg_catalog,"+q.Get("search_path"))
	u.RawQuery = q.Encode()
	// Exercise connection-local name resolution without running schema bootstrap
	// in pg_catalog. Both connections resolve the same existing aggregate table.
	other, err := pgx.Connect(context.Background(), u.String())
	require.NoError(t, err)
	defer other.Close(context.Background())
	var originalTable, aliasTable int64
	require.NoError(t, b.db.QueryRow(`SELECT 'sysbox_lifecycle_aggregates'::regclass::oid::bigint`).Scan(&originalTable))
	require.NoError(t, other.QueryRow(context.Background(), `SELECT 'sysbox_lifecycle_aggregates'::regclass::oid::bigint`).Scan(&aliasTable))
	require.Equal(t, originalTable, aliasTable)
	unlock, err := r.LockExecution(context.Background(), "lab")
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	second, err := lockExecutionPostgres(ctx, u.String(), "lab")
	if second != nil {
		defer second()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
