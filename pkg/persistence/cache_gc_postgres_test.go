package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

func cachePGFixture(t *testing.T) (*Repository, CacheGCPlan) {
	t.Helper()
	ctx := context.Background()
	r := resetTestRepository(t, "postgres")
	_, schemaErr := r.backend.(*sqlBackend).db.Exec(`CREATE TABLE sysbox_global_revisions(workspace TEXT NOT NULL DEFAULT '',id TEXT PRIMARY KEY,data JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	require.NoError(t, schemaErr)
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("orphan")))
	require.NoError(t, os.Mkdir(filepath.Join(root, key), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, key, "blob"), []byte("orphan"), 0600))
	plan, err := r.PlanCacheGC(ctx, root, key+"/blob")
	require.NoError(t, err)
	return r, plan
}
func TestPostgresCacheReservationSurvivesConnectionLoss(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	b := r.backend.(*sqlBackend)
	writer, err := OpenPostgres(ctx, b.dsn)
	require.NoError(t, err)
	defer writer.Close()
	// A separate pool and raw SQL both represent writers unaware of the lock.
	_, err = writer.LoadState(ctx, "new-topology")
	require.Error(t, err)
	_, err = b.db.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=data WHERE topology='lab'")
	require.Error(t, err)
	_, err = b.db.ExecContext(ctx, "DELETE FROM sysbox_lifecycle_aggregates WHERE topology='lab'")
	require.Error(t, err)
	conn, err := b.db.Conn(ctx)
	require.NoError(t, err)
	var pid int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	var killed bool
	require.NoError(t, writer.backend.(*sqlBackend).db.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&killed))
	require.True(t, killed)
	_ = conn.Close()
	require.NoError(t, r.Close())
	require.Error(t, writer.ApplyCacheGC(ctx, plan), "pending reservation cannot replay deletion")
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
	require.Error(t, writer.ReconcileCacheGC(ctx, plan), "missing durable done receipt is not success")
}
func TestPostgresCacheApplyReleasesOnlyAfterDurableReceipt(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.ApplyCacheGC(ctx, plan))
	require.NoFileExists(t, filepath.Join(plan.Root, plan.Path))
	require.FileExists(t, filepath.Join(plan.Root, ".sysbox-gc", plan.ID+".done"))
	_, err := r.LoadState(ctx, "new-topology")
	require.NoError(t, err)
	require.NoError(t, r.ReconcileCacheGC(ctx, plan))
	require.NoError(t, os.WriteFile(filepath.Join(plan.Root, plan.Path), []byte("orphan"), 0600))
	require.Error(t, r.ApplyCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
}

func TestPostgresCacheReconcilesLostCompletionWithoutDeletingAgain(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	// Reproduce a deletion that completed its durable local receipt but lost
	// database connectivity before releasing the global reservation.
	require.NoError(t, os.Remove(filepath.Join(plan.Root, plan.Path)))
	require.NoError(t, syncCacheDir(filepath.Dir(filepath.Join(plan.Root, plan.Path))))
	f, err := os.OpenFile(filepath.Join(journal, plan.ID+".done"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.Write(raw)
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())
	require.NoError(t, syncCacheDir(journal))
	_, err = r.LoadState(ctx, "new")
	require.Error(t, err)
	b := r.backend.(*sqlBackend)
	cold, err := OpenPostgres(ctx, b.dsn)
	require.NoError(t, err)
	defer cold.Close()
	require.NoError(t, cold.ReconcileCacheGC(ctx, plan))
	_, err = cold.LoadState(ctx, "new")
	require.NoError(t, err)
}
func TestPostgresCacheFenceRejectsOldSnapshotWriters(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	b := r.backend.(*sqlBackend)
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	c, err := b.db.Conn(ctx)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ")
	require.NoError(t, err)
	_, err = c.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=data")
	require.Error(t, err)
	_, err = c.ExecContext(ctx, "ROLLBACK")
	require.NoError(t, err)
	_, err = c.ExecContext(ctx, "TRUNCATE sysbox_lifecycle_aggregates")
	require.Error(t, err)
}

func TestPostgresCacheQueuedWriterSeesCommittedFence(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	b := r.backend.(*sqlBackend)
	setup, err := b.db.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, installCacheFence(ctx, setup))
	require.NoError(t, setup.Close())
	holder, err := b.db.Conn(ctx)
	require.NoError(t, err)
	defer holder.Close()
	_, err = holder.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	defer holder.ExecContext(ctx, "ROLLBACK")
	_, err = holder.ExecContext(ctx, "LOCK TABLE sysbox_lifecycle_aggregates IN EXCLUSIVE MODE")
	require.NoError(t, err)
	writer, err := b.db.Conn(ctx)
	require.NoError(t, err)
	defer writer.Close()
	var pid int
	require.NoError(t, writer.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	result := make(chan error, 1)
	go func() {
		_, err := writer.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=data WHERE topology='lab'")
		result <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := holder.QueryRowContext(ctx, "SELECT wait_event_type='Lock' FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting)
		return err == nil && waiting
	}, time.Second, 10*time.Millisecond)
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	_, err = holder.ExecContext(ctx, "INSERT INTO sysbox_cache_deletions(id,host,plan,state) VALUES($1,'test',$2::jsonb,'pending')", plan.ID, string(raw))
	require.NoError(t, err)
	_, err = holder.ExecContext(ctx, "COMMIT")
	require.NoError(t, err)
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("queued writer did not finish")
	}
}

func TestPostgresCacheCrashHelper(t *testing.T) {
	if os.Getenv("SYSBOX_CACHE_CRASH_HELPER") != "1" {
		return
	}
	var plan CacheGCPlan
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("SYSBOX_CACHE_CRASH_PLAN")), &plan))
	r, err := OpenPostgres(context.Background(), os.Getenv("SYSBOX_CACHE_CRASH_DSN"))
	require.NoError(t, err)
	require.NoError(t, r.reservePostgresCache(context.Background(), plan))
	os.Exit(71) // No deferred close and no deletion: durable reservation remains.
}
func TestPostgresCacheProcessCrashNeverReplaysUnlink(t *testing.T) {
	r, plan := cachePGFixture(t)
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	child := exec.Command(os.Args[0], "-test.run=^TestPostgresCacheCrashHelper$")
	child.Env = append(os.Environ(), "SYSBOX_CACHE_CRASH_HELPER=1", "SYSBOX_CACHE_CRASH_PLAN="+string(raw), "SYSBOX_CACHE_CRASH_DSN="+r.backend.(*sqlBackend).dsn)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, string(output))
	require.Equal(t, 71, exit.ExitCode())
	ctx := context.Background()
	_, err = r.LoadState(ctx, "another")
	require.Error(t, err)
	require.Error(t, r.ApplyCacheGC(ctx, plan))
	require.Error(t, r.ReconcileCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
}

func TestPostgresCacheIntentRecoveryRequiresAbsenceAndOriginalHost(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+".intent"), raw, 0600))
	require.Error(t, r.RecoverCacheGCIntent(ctx, plan), "present file is not deletion completion")
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
	require.NoError(t, os.Remove(filepath.Join(plan.Root, plan.Path)))
	b := r.backend.(*sqlBackend)
	_, err = b.db.ExecContext(ctx, "UPDATE sysbox_cache_deletions SET host='foreign' WHERE id=$1", plan.ID)
	require.NoError(t, err)
	require.Error(t, r.RecoverCacheGCIntent(ctx, plan))
	require.NoFileExists(t, filepath.Join(journal, plan.ID+".done"))
	host, err := cacheHostID()
	require.NoError(t, err)
	_, err = b.db.ExecContext(ctx, "UPDATE sysbox_cache_deletions SET host=$1 WHERE id=$2", host, plan.ID)
	require.NoError(t, err)
	require.NoError(t, r.RecoverCacheGCIntent(ctx, plan))
	require.FileExists(t, filepath.Join(journal, plan.ID+".done"))
	require.NoFileExists(t, filepath.Join(journal, plan.ID+".intent"))
	_, err = r.LoadState(ctx, "new-after-recovery")
	require.NoError(t, err)
	require.NoError(t, r.RecoverCacheGCIntent(ctx, plan), "exact receipt replay only releases, never deletes")
}

func TestCacheRecoveryAfterProcessExitAtUnlinkBoundary(t *testing.T) {
	if os.Getenv("SYSBOX_CACHE_UNLINK_CHILD") == "1" {
		var plan CacheGCPlan
		require.NoError(t, json.Unmarshal([]byte(os.Getenv("SYSBOX_CACHE_UNLINK_PLAN")), &plan))
		lock := flock.New(filepath.Join(plan.Root, ".sysbox-gc", "lock"))
		held, err := lock.TryLock()
		require.NoError(t, err)
		require.True(t, held)
		raw, err := json.Marshal(plan)
		require.NoError(t, err)
		f, err := os.OpenFile(filepath.Join(plan.Root, ".sysbox-gc", plan.ID+".intent"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		require.NoError(t, err)
		_, err = f.Write(raw)
		require.NoError(t, err)
		require.NoError(t, f.Sync())
		require.NoError(t, f.Close())
		require.NoError(t, syncCacheDir(filepath.Join(plan.Root, ".sysbox-gc")))
		require.NoError(t, os.Remove(filepath.Join(plan.Root, plan.Path)))
		os.Exit(72)
	}
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	require.NoError(t, os.Mkdir(filepath.Join(plan.Root, ".sysbox-gc"), 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	child := exec.Command(os.Args[0], "-test.run=^TestCacheRecoveryAfterProcessExitAtUnlinkBoundary$")
	child.Env = append(os.Environ(), "SYSBOX_CACHE_UNLINK_CHILD=1", "SYSBOX_CACHE_UNLINK_PLAN="+string(raw))
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, string(out))
	require.Equal(t, 72, exit.ExitCode())
	require.NoError(t, r.RecoverCacheGCIntent(ctx, plan))
	require.FileExists(t, filepath.Join(plan.Root, ".sysbox-gc", plan.ID+".done"))
	_, err = r.LoadState(ctx, "new")
	require.NoError(t, err)
}
