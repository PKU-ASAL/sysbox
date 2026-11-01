package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestCacheGCProtectsUnboundGlobalRevisionContent(t *testing.T) {
	ctx := context.Background()
	r := resetTestRepository(t, "sqlite")
	db := r.backend.(*sqlBackend).db
	_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(revision TEXT PRIMARY KEY,files TEXT,size INTEGER,created_at TEXT)`)
	require.NoError(t, err)
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	blob := []byte("protected artifact")
	sum := fmt.Sprintf("%x", sha256.Sum256(blob))
	require.NoError(t, os.Mkdir(filepath.Join(root, sum), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, sum, "blob"), blob, 0600))
	plan, err := r.PlanCacheGC(ctx, root, sum+"/blob")
	require.NoError(t, err)
	files := map[string][]byte{"field.sysbox.hcl": []byte("# unbound"), "image.bin": blob}
	raw, err := json.Marshal(files)
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO sysbox_global_revisions VALUES(?,?,?,?)", controlplane.ComputeRevisionDigest(files), string(raw), len(blob)+9, time.Now().Format(time.RFC3339))
	require.NoError(t, err)
	_, err = r.PlanCacheGC(ctx, root, sum+"/blob")
	require.Error(t, err)
	require.Error(t, r.ApplyCacheGC(ctx, plan))
	require.FileExists(t, filepath.Join(root, sum, "blob"))
}

func TestPostgresCacheFencesGlobalRevisionPublication(t *testing.T) {
	r, plan := cachePGFixture(t)
	ctx := context.Background()
	db := r.backend.(*sqlBackend).db
	require.NoError(t, r.reservePostgresCache(ctx, plan))
	files := map[string][]byte{"field.sysbox.hcl": []byte("# late publication")}
	rev := controlplane.GlobalRevision{Revision: controlplane.ComputeRevisionDigest(files), Files: files, CreatedAt: time.Now()}
	raw, err := json.Marshal(rev)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO sysbox_global_revisions(id,data) VALUES($1,$2::jsonb)", rev.Revision, string(raw))
	require.Error(t, err)
	_, err = db.ExecContext(ctx, "TRUNCATE sysbox_global_revisions")
	require.Error(t, err)
	require.FileExists(t, filepath.Join(plan.Root, plan.Path))
}
func TestGlobalRevisionInventoryRejectsCorruptAndIncludesTextReferences(t *testing.T) {
	ctx := context.Background()
	r := resetTestRepository(t, "sqlite")
	db := r.backend.(*sqlBackend).db
	_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(revision TEXT PRIMARY KEY,files TEXT)`)
	require.NoError(t, err)
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	require.NoError(t, os.Mkdir(filepath.Join(root, sum), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, sum, "blob"), []byte("image"), 0600))
	files := map[string][]byte{"field.sysbox.hcl": []byte(`sha256 = "` + sum + `"`)}
	raw, err := json.Marshal(files)
	require.NoError(t, err)
	id := controlplane.ComputeRevisionDigest(files)
	_, err = db.Exec("INSERT INTO sysbox_global_revisions VALUES(?,?)", id, string(raw))
	require.NoError(t, err)
	_, err = r.PlanCacheGC(ctx, root, sum+"/blob")
	require.Error(t, err, "decoded file contents protect referenced digest")
	_, err = db.Exec("UPDATE sysbox_global_revisions SET files='{}'")
	require.NoError(t, err)
	_, err = r.PlanCacheGC(ctx, root, sum+"/blob")
	require.ErrorIs(t, err, ErrCorruptRecord)
}
