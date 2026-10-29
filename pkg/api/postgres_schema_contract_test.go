package api

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaRejectsUnsupportedWithoutMutation(t *testing.T) {
	for _, version := range []int{0, 6, 99} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx := context.Background()
			dsn := isolatedPostgresTestDSN(t)
			conn, err := pgx.Connect(ctx, controlstore.DsnWithoutSysboxQuery(dsn))
			require.NoError(t, err)
			defer conn.Close(ctx)
			_, err = conn.Exec(ctx, `CREATE TABLE sysbox_runs(id TEXT PRIMARY KEY); INSERT INTO sysbox_runs VALUES('preserved')`)
			require.NoError(t, err)
			if version != 0 {
				_, err = conn.Exec(ctx, `CREATE TABLE sysbox_api_schema_migrations(name TEXT PRIMARY KEY,version INTEGER NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
				require.NoError(t, err)
				_, err = conn.Exec(ctx, `INSERT INTO sysbox_api_schema_migrations(name,version) VALUES('api',$1)`, version)
				require.NoError(t, err)
			}
			s := &controlstore.Postgres{DSN: dsn}
			_, err = s.PoolFor(ctx)
			require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
			require.ErrorContains(t, err, "rebuild")
			var tables int
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema()`).Scan(&tables))
			want := 1
			if version != 0 {
				want++
			}
			require.Equal(t, want, tables)
			var id string
			require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM sysbox_runs`).Scan(&id))
			require.Equal(t, "preserved", id)
			if version != 0 {
				var actual int
				require.NoError(t, conn.QueryRow(ctx, `SELECT version FROM sysbox_api_schema_migrations WHERE name='api'`).Scan(&actual))
				require.Equal(t, version, actual)
			}
		})
	}
}

func TestPostgresSchemaConcurrentFreshOpenAndReopen(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedPostgresTestDSN(t)
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &controlstore.Postgres{DSN: dsn}
			pool, err := s.PoolFor(ctx)
			if err == nil {
				defer pool.Close()
				var version int
				version, err = s.SchemaVersion(ctx)
				if err == nil && version != controlstore.SchemaVersion {
					err = fmt.Errorf("version %d", version)
				}
			}
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	s := &controlstore.Postgres{DSN: dsn}
	pool, err := s.PoolFor(ctx)
	require.NoError(t, err)
	defer pool.Close()
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM sysbox_api_schema_migrations`).Scan(&count))
	require.Equal(t, 1, count, "current schema stores one version, not a migration history")
}

func TestPostgresCurrentSchemaDoesNotRepairMissingTable(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedPostgresTestDSN(t)
	s := &controlstore.Postgres{DSN: dsn}
	pool, err := s.PoolFor(ctx)
	require.NoError(t, err)
	defer pool.Close()
	_, err = pool.Exec(ctx, `DROP TABLE sysbox_execution_authority`)
	require.NoError(t, err)
	_, err = (&controlstore.Postgres{DSN: dsn}).PoolFor(ctx)
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
	var missing bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('sysbox_execution_authority') IS NULL`).Scan(&missing))
	require.True(t, missing)
}
