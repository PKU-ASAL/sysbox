package api

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRevisionRetentionProtectsHistoricalRunsAndKeepsNewest(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		now := time.Now().UTC()
		var revisions []string
		for i, text := range []string{"old", "middle", "new"} {
			files := map[string][]byte{"field.sysbox.hcl": []byte(text)}
			id := controlplane.ComputeRevisionDigest(files)
			revisions = append(revisions, id)
			require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, Size: len(text), CreatedAt: now.Add(time.Duration(i-10) * time.Hour)}))
		}
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "historical", Topology: "lab", Revision: revisions[0], Status: controlplane.RunDone, QueuedAt: now.Add(-8 * time.Hour), EndedAt: now.Add(-7 * time.Hour)}))
		plan, err := s.planRevisionRetention(ctx, RevisionRetentionPolicy{KeepLast: 1, OlderThan: now, Limit: 10})
		require.NoError(t, err)
		require.Equal(t, []string{revisions[1]}, plan.Candidates)
		require.NotEmpty(t, plan.Digest)
		require.False(t, plan.DeletionAuthorized)
		again, err := s.planRevisionRetention(ctx, RevisionRetentionPolicy{KeepLast: 1, OlderThan: now, Limit: 10})
		require.NoError(t, err)
		require.Equal(t, plan.Digest, again.Digest)
		for _, id := range revisions {
			_, err := store.GetGlobalRevision(ctx, id)
			require.NoError(t, err, "plan must not mutate registry")
		}
	})
}
func TestRevisionRetentionRejectsCorruptContent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	files := map[string][]byte{"field.sysbox.hcl": []byte("old")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, s.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: map[string][]byte{"field.sysbox.hcl": []byte("changed")}, CreatedAt: time.Now().Add(-time.Hour)}))
	_, err := s.planRevisionRetention(ctx, RevisionRetentionPolicy{KeepLast: 1, OlderThan: time.Now(), Limit: 10})
	require.Error(t, err)
}

func TestRevisionRetentionProtectsSnapshotOnlyAndNestedRevision(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		now := time.Now().UTC()
		files := map[string][]byte{"field.sysbox.hcl": []byte("snapshot-only")}
		id := controlplane.ComputeRevisionDigest(files)
		require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: now.Add(-time.Hour)}))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
		st, err := manager.Load()
		require.NoError(t, err)
		st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "test"), OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, Driver: "docker", ExternalID: "container", Attributes: state.MustAttributes(map[string]any{"revision": id})}}
		require.NoError(t, manager.Save(st))
		_, err = manager.Snapshot(ctx, "retain revision")
		require.NoError(t, err)
		st.Resources = nil
		require.NoError(t, manager.Save(st))
		plan, err := s.planRevisionRetention(ctx, RevisionRetentionPolicy{KeepLast: 0, OlderThan: now, Limit: 10})
		require.NoError(t, err)
		require.Empty(t, plan.Candidates)
	})
}
func TestRevisionRetentionHTTPIsStrictAndPreviewOnly(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC()
	files := map[string][]byte{"field.sysbox.hcl": []byte("orphan")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, s.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: now.Add(-time.Hour)}))
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/revisions/retention-plan", strings.NewReader(body)))
		return w
	}
	raw, err := json.Marshal(RevisionRetentionPolicy{KeepLast: 0, OlderThan: now, Limit: 1})
	require.NoError(t, err)
	good := call(string(raw))
	require.Equal(t, 200, good.Code, good.Body.String())
	var plan RevisionRetentionPlan
	require.NoError(t, json.Unmarshal(good.Body.Bytes(), &plan))
	require.Equal(t, []string{id}, plan.Candidates)
	require.False(t, plan.DeletionAuthorized)
	require.Equal(t, 400, call(string(raw)+" {}").Code)
	require.Equal(t, 400, call(`{"delete":true}`).Code)
	require.Equal(t, 409, call(`{"keep_last":0,"limit":129,"older_than":"2000-01-01T00:00:00Z"}`).Code)
	_, err = s.apiStore.GetGlobalRevision(ctx, id)
	require.NoError(t, err)
}

func TestRevisionRetentionRejectsLegacyAndScopedCatalog(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		s := NewServer(t.TempDir(), t.TempDir())
		require.NoError(t, os.MkdirAll(filepath.Join(s.runsDir, "old"), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(s.runsDir, "old", "state.json"), []byte(`{"version":1}`), 0600))
		_, err := s.planRevisionRetention(context.Background(), RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 1})
		require.Error(t, err)
	})
	t.Run("scoped", func(t *testing.T) {
		s := NewServer(t.TempDir(), t.TempDir())
		s.stateBackend = "sqlite://" + filepath.Join(t.TempDir(), "catalog.db") + "?topology=lab"
		_, err := s.lifecycleRevisionReferences(context.Background())
		require.Error(t, err)
	})
}

func TestRevisionRetentionRejectsOversizedRunHistory(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	dir := filepath.Join(s.runsDir, "lab")
	require.NoError(t, os.MkdirAll(dir, 0700))
	f, err := os.Create(filepath.Join(dir, "runs.jsonl"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxRevisionInventoryBytes+1))
	require.NoError(t, f.Close())
	_, err = s.planRevisionRetention(context.Background(), RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 1})
	require.Error(t, err)
}

func TestRevisionRetentionRejectsNullRunHistory(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	dir := filepath.Join(s.runsDir, "lab")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "runs.jsonl"), []byte("null\n"), 0600))
	_, err := s.planRevisionRetention(context.Background(), RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 1})
	require.Error(t, err)
}
