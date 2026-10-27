package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestStage2RevisionDeletionRejectsFileScopedAndBulkRequests(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	s := NewServerWithConfig(cfg)
	files := map[string][]byte{"field.sysbox.hcl": []byte("retained")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, s.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	post := func(body string) int {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/revisions/delete", strings.NewReader(body)))
		return w.Code
	}
	raw, err := json.Marshal(map[string]any{"revision": id, "older_than": time.Now()})
	require.NoError(t, err)
	require.Equal(t, 200, post(string(raw)))
	_, err = s.apiStore.GetGlobalRevision(ctx, id)
	require.Error(t, err, "retired file is no longer visible")
	require.Equal(t, 400, post(`{"revisions":["`+id+`"],"older_than":"2000-01-01T00:00:00Z"}`))
	path := filepath.Join(t.TempDir(), "uncreated.db")
	s.stateBackend = "sqlite://" + path + "?topology=lab"
	require.Equal(t, 409, post(string(raw)))
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "scoped refusal must precede database bootstrap")
}

func TestStage2RevisionDeletionRetainsSnapshotAndHistoricalRun(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		if _, local := store.(*localAPIStore); local {
			return
		} // file refusal covered separately
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		files := map[string][]byte{"field.sysbox.hcl": []byte("snapshot-retained")}
		id := controlplane.ComputeRevisionDigest(files)
		require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
		v, err := repo.BeginGCSource(ctx, "lab", "source", st.TopologyUID, 0)
		require.NoError(t, err)
		manifest := persistence.GCSourceManifest{Domains: map[string][]string{"revision": {id}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "source", st.TopologyUID, v, manifest))
		snapshot, err := state.NewManagerWithBackend(repo.StateBackend("lab")).Snapshot(ctx, "retain")
		require.NoError(t, err)
		v, err = repo.BeginGCSource(ctx, "lab", "source", st.TopologyUID, 2)
		require.NoError(t, err)
		manifest.Domains["revision"] = []string{}
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "source", st.TopologyUID, v, manifest))
		raw, err := json.Marshal(map[string]any{"revision": id, "older_than": time.Now()})
		require.NoError(t, err)
		post := func() *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/revisions/delete", strings.NewReader(string(raw))))
			return w
		}
		snapshotRejection := post()
		require.Equal(t, 409, snapshotRejection.Code)
		require.Contains(t, snapshotRejection.Body.String(), "retained lifecycle revision")
		_, err = store.GetGlobalRevision(ctx, id)
		require.NoError(t, err)
		require.NoError(t, repo.StateBackend("lab").(state.SnapshotDeleter).DeleteSnapshot(ctx, snapshot.ID))
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "history", Topology: "lab", Revision: id, Status: controlplane.RunDone, QueuedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}))
		runRejection := post()
		require.Equal(t, 409, runRejection.Code)
		require.Contains(t, runRejection.Body.String(), "revision retained by Run history")
		_, err = store.GetGlobalRevision(ctx, id)
		require.NoError(t, err)
	})
}
