package controlstore

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLifecycleRepositoriesShareDatabaseButKeepTopologyBoundaries(t *testing.T) {
	var cache LifecycleRepositories
	defer cache.Close()
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "lifecycle.db") + "?topology={topology}"
	var first any
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("topology-%d", i)
		repo, err := cache.Open(t.TempDir(), dsn, name)
		require.NoError(t, err)
		_, err = repo.LoadState(context.Background(), name)
		require.NoError(t, err)
		_, err = repo.LoadState(context.Background(), "foreign")
		require.Error(t, err)
		if i == 0 {
			first = repo
		}
	}
	require.Len(t, cache.databaseRepositories, 1, "one physical database must own one pool")
	again, err := cache.Open(t.TempDir(), dsn, "topology-0")
	require.NoError(t, err)
	require.Same(t, first, again)
	plain, err := cache.Open(t.TempDir(), strings.Split(dsn, "?")[0], "topology-0")
	require.NoError(t, err)
	require.Same(t, again, plain, "selector spelling must not duplicate views")
	_, err = plain.LoadState(context.Background(), "topology-1")
	require.Error(t, err)
	// Closing a borrowed topology view must not close other topology pools.
	require.NoError(t, again.Close())
	other, err := cache.Open(t.TempDir(), dsn, "topology-1")
	require.NoError(t, err)
	_, err = other.LoadState(context.Background(), "topology-1")
	require.NoError(t, err)
	_, err = cache.Open(t.TempDir(), "sqlite://"+filepath.Join(t.TempDir(), "other.db")+"?topology=fixed", "wrong")
	require.Error(t, err)
	require.NoError(t, cache.Close())
	require.Empty(t, cache.Repositories)
	require.Empty(t, cache.databaseRepositories)
}

func TestLifecyclePostgresConnectionsBoundedAcrossTopologyHistory(t *testing.T) {
	pool := authorityLockPool(t)
	ctx := context.Background()
	cfg := pool.Config().ConnConfig
	// Use the isolated schema created by authorityLockPool; only the topology
	// selector varies, while application_name identifies this cache's sessions.
	dsn := cfg.ConnString()
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "application_name=lifecycle-pool-regression&topology={topology}"
	var cache LifecycleRepositories
	defer cache.Close()
	for i := 0; i < 120; i++ {
		name := fmt.Sprintf("history-%d", i)
		repo, err := cache.Open(t.TempDir(), dsn, name)
		require.NoError(t, err)
		_, err = repo.LoadState(ctx, name)
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name='lifecycle-pool-regression'").Scan(&count))
	require.LessOrEqual(t, count, 8)
	require.Len(t, cache.databaseRepositories, 1)
	require.NoError(t, cache.Close())
	require.Eventually(t, func() bool {
		err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name='lifecycle-pool-regression'").Scan(&count)
		return err == nil && count == 0
	}, 3*time.Second, 20*time.Millisecond)
}
