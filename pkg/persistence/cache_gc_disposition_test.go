package persistence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheDispositionAbortsExactPresentIntentWithoutDeleting(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+".intent"), raw, 0600))
	report, err := r.InspectCacheGC(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, "abort", report.Action)
	require.NoError(t, r.AbortCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
	require.NoFileExists(t, filepath.Join(journal, plan.ID+".done"))
	require.FileExists(t, filepath.Join(journal, plan.ID+".aborted"))
	require.NoError(t, r.AbortCacheGC(ctx, plan))
	_, err = r.LoadState(ctx, "after-abort")
	require.NoError(t, err)
	require.Error(t, r.ApplyCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
}
func TestCacheDispositionRejectsChangedTarget(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+".intent"), raw, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(plan.Root, plan.Path), []byte("replacement"), 0600))
	report, err := r.InspectCacheGC(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, "manual_review", report.Action)
	require.Error(t, r.AbortCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(journal, plan.ID+".intent"))
}

func TestCacheDispositionRejectsConflictingReceipt(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+".intent"), raw, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+".aborted"), []byte("corrupt"), 0600))
	report, err := r.InspectCacheGC(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, "manual_review", report.Action)
	require.Error(t, r.AbortCacheGC(ctx, plan))
	_, err = r.LoadState(ctx, "must-remain-blocked")
	require.Error(t, err)
}

func TestCacheDispositionAbortsBeforeIntentPublication(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	require.NoError(t, os.Mkdir(filepath.Join(plan.Root, ".sysbox-gc"), 0700))
	report, err := r.InspectCacheGC(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, "abort", report.Action)
	require.NoError(t, r.AbortCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
	var outcome string
	require.NoError(t, r.backend.(*sqlBackend).db.QueryRowContext(ctx, "SELECT outcome FROM sysbox_cache_deletions WHERE id=$1", plan.ID).Scan(&outcome))
	require.Equal(t, "aborted", outcome)
}

func TestCacheDispositionConflictingTerminalsNeverUnlock(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	for _, ext := range []string{".aborted", ".done"} {
		require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+ext), raw, 0600))
	}
	report, err := r.InspectCacheGC(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, "manual_review", report.Action)
	require.Error(t, r.AbortCacheGC(ctx, plan))
	require.NoError(t, os.Remove(filepath.Join(plan.Root, plan.Path)))
	require.Error(t, r.ReconcileCacheGC(ctx, plan))
	require.Error(t, r.RecoverCacheGCIntent(ctx, plan))
	_, err = r.LoadState(ctx, "blocked")
	require.Error(t, err)
}
