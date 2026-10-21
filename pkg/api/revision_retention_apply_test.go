package api

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRevisionRetentionApplyDeletesBatchAndRejectsChangedPreview(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		now := time.Now().UTC()
		ids := []string{}
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
		require.NoError(t, err)
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: now.Add(time.Hour)}))
		for i, text := range []string{"first", "second", "latest"} {
			files := map[string][]byte{"field.sysbox.hcl": []byte(text)}
			id := controlplane.ComputeRevisionDigest(files)
			ids = append(ids, id)
			require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: now.Add(time.Duration(i-5) * time.Hour)}))
		}
		policy := RevisionRetentionPolicy{KeepLast: 1, OlderThan: now, Limit: 2}
		plan, err := s.planRevisionRetention(ctx, policy)
		require.NoError(t, err)
		require.Len(t, plan.Candidates, 2)
		apply := func(plan RevisionRetentionPlan) *httptest.ResponseRecorder {
			raw, err := json.Marshal(plan)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/revisions/retention-apply", strings.NewReader(string(raw))))
			return w
		}
		changed := plan
		changed.Digest = "changed"
		require.Equal(t, 409, apply(changed).Code)
		response := apply(plan)
		if _, local := store.(*localAPIStore); local {
			require.Equal(t, 409, response.Code)
			return
		}
		require.Equal(t, 200, response.Code, response.Body.String())
		for _, id := range ids[:2] {
			_, err = store.GetGlobalRevision(ctx, id)
			require.Error(t, err)
		}
		_, err = store.GetGlobalRevision(ctx, ids[2])
		require.NoError(t, err)
		require.Equal(t, 409, apply(plan).Code, "old preview cannot be applied again")
	})
}

func TestRevisionRetentionApplyRechecksActualLateRun(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		if _, local := store.(*localAPIStore); local {
			return
		}
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		now := time.Now().UTC()
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
		require.NoError(t, err)
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: now.Add(time.Hour)}))
		files := map[string][]byte{"field.sysbox.hcl": []byte("late referenced")}
		id := controlplane.ComputeRevisionDigest(files)
		require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: now.Add(-time.Hour)}))
		plan, err := s.planRevisionRetention(ctx, RevisionRetentionPolicy{KeepLast: 0, OlderThan: now, Limit: 1})
		require.NoError(t, err)
		require.Equal(t, []string{id}, plan.Candidates)
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: now}))
		raw, err := json.Marshal(plan)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/revisions/retention-apply", strings.NewReader(string(raw))))
		require.Equal(t, 409, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "preview changed")
		_, err = store.GetGlobalRevision(ctx, id)
		require.NoError(t, err)
	})
}
