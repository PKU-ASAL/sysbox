package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGCCacheCLIPlanApplyUsesCanonicalCatalog(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "db")
	repo, err := persistence.OpenSQLite(db)
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("blob")))
	require.NoError(t, os.Mkdir(filepath.Join(root, key), 0700))
	target := filepath.Join(root, key, "blob")
	require.NoError(t, os.WriteFile(target, []byte("blob"), 0600))
	run := func(args ...string) ([]byte, error) {
		cmd := newGCCacheCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(ctx)
		return out.Bytes(), err
	}
	raw, err := run("--catalog", "sqlite://"+db, "--cache-root", root, "--path", key+"/blob")
	require.NoError(t, err)
	require.FileExists(t, target)
	var plan persistence.CacheGCPlan
	require.NoError(t, json.Unmarshal(raw, &plan))
	path := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	_, err = run("--catalog", "sqlite://"+db, "--apply-plan", path)
	require.NoError(t, err)
	require.NoFileExists(t, target)
	// Simulate an interrupted receipt publication with target already absent.
	journal := filepath.Join(root, ".sysbox-gc")
	require.NoError(t, os.Rename(filepath.Join(journal, plan.ID+".done"), filepath.Join(journal, plan.ID+".intent")))
	_, err = run("--catalog", "sqlite://"+db, "--recover-intent-plan", path)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(journal, plan.ID+".done"))
	require.NoFileExists(t, target)

}

func TestGCCacheCLIRefusesConflictingApplyAndReconcile(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db")
	r, err := persistence.OpenSQLite(db)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	cmd := newGCCacheCommand()
	cmd.SetArgs([]string{"--catalog", "sqlite://" + db, "--apply-plan", "a", "--reconcile-plan", "b"})
	require.ErrorContains(t, cmd.Execute(), "mutually exclusive")
}

func TestGCCacheCLIInspectsAndAbortsWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "db")
	r, err := persistence.OpenSQLite(db)
	require.NoError(t, err)
	defer r.Close()
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("keep")))
	require.NoError(t, os.Mkdir(filepath.Join(root, key), 0700))
	target := filepath.Join(root, key, "blob")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0600))
	plan, err := r.PlanCacheGC(ctx, root, key+"/blob")
	require.NoError(t, err)
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	planPath := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(planPath, raw, 0600))
	journal := filepath.Join(root, ".sysbox-gc")
	require.NoError(t, os.Mkdir(journal, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(journal, plan.ID+".intent"), raw, 0600))
	run := func(flag string) []byte {
		cmd := newGCCacheCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--catalog", "sqlite://" + db, flag, planPath})
		require.NoError(t, cmd.ExecuteContext(ctx))
		return out.Bytes()
	}
	var report persistence.CacheGCDisposition
	require.NoError(t, json.Unmarshal(run("--inspect-plan"), &report))
	require.Equal(t, "abort", report.Action)
	var result map[string]any
	require.NoError(t, json.Unmarshal(run("--abort-plan"), &result))
	require.Equal(t, false, result["deleted"])
	require.Equal(t, true, result["aborted"])
	require.FileExists(t, target)
}
