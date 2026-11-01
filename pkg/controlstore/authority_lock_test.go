package controlstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func authorityLockPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SYSBOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("authority_lock_%d", time.Now().UnixNano())
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pool, err := pgxpool.New(ctx, dsn+sep+"search_path="+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, err := conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err)
		require.NoError(t, conn.Close(context.Background()))
	})
	_, err = pool.Exec(ctx, `CREATE TABLE sysbox_execution_authority(id text PRIMARY KEY,data jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now())`)
	require.NoError(t, err)
	return pool
}

func TestAuthorityExistingRowDoesNotAttemptInsert(t *testing.T) {
	pool := authorityLockPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	raw, err := lockPostgresExecutionAuthority(ctx, tx)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.NoError(t, tx.Commit(ctx))
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_authority_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unexpected authority insert'; END $$; CREATE TRIGGER reject_insert BEFORE INSERT ON sysbox_execution_authority FOR EACH ROW EXECUTE FUNCTION reject_authority_insert()`)
	require.NoError(t, err)
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	again, err := lockPostgresExecutionAuthority(ctx, tx)
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(again))
	require.NoError(t, tx.Commit(ctx))
}

func TestAuthorityFirstCreationSerializesAndPreservesWinner(t *testing.T) {
	pool := authorityLockPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer first.Rollback(context.Background())
	raw, err := lockPostgresExecutionAuthority(ctx, first)
	require.NoError(t, err)
	// A real ledger mutation stands for the first transaction's committed result.
	memory := &GrantTransaction{Raw: raw}
	_, err = persistence.NewExecutionGrantRepository(memory).OpenSession(ctx, "host", "boot", "")
	require.NoError(t, err)
	_, err = first.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb WHERE id='authority'", string(memory.Raw))
	require.NoError(t, err)
	second, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer second.Rollback(context.Background())
	firstPID, secondPID := first.Conn().PgConn().PID(), second.Conn().PgConn().PID()
	result := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() { v, e := lockPostgresExecutionAuthority(ctx, second); result <- v; errs <- e }()
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(ctx, "SELECT $1::int = ANY(pg_blocking_pids($2::int))", firstPID, secondPID).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "contender must actually wait for first initializer in PostgreSQL")
	require.NoError(t, first.Commit(ctx))
	require.NoError(t, <-errs)
	require.JSONEq(t, string(memory.Raw), string(<-result))
	require.NoError(t, second.Commit(ctx))
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM sysbox_execution_authority").Scan(&count))
	require.Equal(t, 1, count)
}
