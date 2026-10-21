package persistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestFederatedRevisionRetirementIsDurableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenSQLite(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	defer repo.Close()
	_, err = repo.backend.(*sqlBackend).db.Exec(`CREATE TABLE sysbox_global_revisions(revision TEXT PRIMARY KEY,files TEXT,created_at TEXT); CREATE TABLE sysbox_runs(id TEXT,revision TEXT)`)
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	version, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, version, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	// Independent SQL consumer contains no active topology references. A registry
	// retirement still installs admission fences when its local copy is absent.
	id := controlplane.ComputeRevisionDigest(map[string][]byte{"field.sysbox.hcl": []byte("remote")})
	require.NoError(t, repo.RetireFederatedRevisions(ctx, "registry", "request", []string{id}, time.Now()))
	require.NoError(t, repo.RetireFederatedRevisions(ctx, "registry", "request", []string{id}, time.Now()))
	other := controlplane.ComputeRevisionDigest(map[string][]byte{"field.sysbox.hcl": []byte("other")})
	require.Error(t, repo.RetireFederatedRevisions(ctx, "registry", "request", []string{other}, time.Now()))
}
