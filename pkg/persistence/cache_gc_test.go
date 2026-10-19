package persistence

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGlobalCacheGCProtectsReferencesAndRejectsStalePlan(t *testing.T) {
	ctx := context.Background()
	r, err := OpenSQLite(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	defer r.Close()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("blob")))
	rel := sum + "/image"
	require.NoError(t, os.Mkdir(filepath.Join(root, sum), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("blob"), 0600))
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	_, err = r.PlanCacheGC(ctx, root, rel)
	require.Error(t, err, "unenrolled catalogs are incomplete")
	v, err := r.BeginGCSource(ctx, "lab", "host-artifacts", st.TopologyUID, 0)
	require.NoError(t, err)
	m := GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host-artifacts", st.TopologyUID, v, m))
	plan, err := r.PlanCacheGC(ctx, root, rel)
	require.NoError(t, err)
	v, err = r.BeginGCSource(ctx, "lab", "host-artifacts", st.TopologyUID, 2)
	require.NoError(t, err)
	m.Domains["artifact"] = []string{"sha256:" + sum}
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host-artifacts", st.TopologyUID, v, m))
	require.Error(t, r.ApplyCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(root, rel))
	_, err = r.PlanCacheGC(ctx, root, rel)
	require.Error(t, err, "content reference protects file")
}
func TestGlobalCacheGCDeletesExactUnreferencedFileAndRetainsReceipt(t *testing.T) {
	for _, kind := range []string{"sqlite"} {
		t.Run(kind, func(t *testing.T) { testCacheGCDelete(t, kind) })
	}
}
func testCacheGCDelete(t *testing.T, kind string) {
	ctx := context.Background()
	r := resetTestRepository(t, kind)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("orphan")))
	rel := sum + "/blob"
	require.NoError(t, os.Mkdir(filepath.Join(root, sum), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("orphan"), 0600))
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host-artifacts", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host-artifacts", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	plan, err := r.PlanCacheGC(ctx, root, rel)
	require.NoError(t, err)
	require.NoError(t, r.ApplyCacheGC(ctx, plan))
	require.NoFileExists(t, filepath.Join(root, rel))
	require.FileExists(t, filepath.Join(root, ".sysbox-gc", plan.ID+".done"))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("orphan"), 0600))
	require.Error(t, r.ApplyCacheGC(ctx, plan), "completion is never permission to delete a recreated path")
	require.FileExists(t, filepath.Join(root, rel))
}

func TestGlobalCacheGCFreezesNewTopologyPublication(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
			require.NoError(t, err)
			require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
			b := r.backend.(*sqlBackend)
			var writer *Repository
			if kind == "sqlite" {
				writer, err = OpenSQLite(b.path)
			} else {
				writer, err = OpenPostgres(ctx, b.dsn)
			}
			require.NoError(t, err)
			defer writer.Close()
			require.NoError(t, r.withCacheCatalog(ctx, func(_ []*aggregate, _ string) error {
				// A second connection cannot insert a new topology while deletion owns
				// the catalog, even though no existing row could lock that topology.
				bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				_, err := writer.LoadState(bounded, "new-topology")
				require.Error(t, err)
				return nil
			}))
			_, err = r.LoadState(ctx, "new-topology")
			require.NoError(t, err)
		})
	}
}
func TestGlobalCacheGCRejectsUnsafeTargetsAndIncompleteIntent(t *testing.T) {
	ctx := context.Background()
	r := resetTestRepository(t, "sqlite")
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("x")))
	require.NoError(t, os.Mkdir(filepath.Join(root, key), 0700))
	file := filepath.Join(root, key, "blob")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0600))
	plan, err := r.PlanCacheGC(ctx, root, key+"/blob")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".sysbox-gc"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sysbox-gc", "crashed.intent"), []byte("partial"), 0600))
	require.ErrorIs(t, r.ApplyCacheGC(ctx, plan), ErrConflict)
	require.FileExists(t, file)
	_, err = r.PlanCacheGC(ctx, root, "../outside")
	require.Error(t, err)
	require.NoError(t, os.Symlink(file, filepath.Join(root, key, "link")))
	_, err = r.PlanCacheGC(ctx, root, key+"/link")
	require.Error(t, err)
	require.NoError(t, os.Link(file, filepath.Join(root, key, "hard")))
	_, err = r.PlanCacheGC(ctx, root, key+"/blob")
	require.Error(t, err)
}

func TestGlobalCacheGCRejectsInvalidRemoteDeletion(t *testing.T) {
	r := resetTestRepository(t, "postgres")
	require.ErrorIs(t, r.ApplyCacheGC(context.Background(), CacheGCPlan{}), ErrInvalidRecord)
}
