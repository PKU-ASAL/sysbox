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
}
