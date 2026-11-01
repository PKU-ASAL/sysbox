package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGCSourceHTTPPublicationIsDurableAndStrict(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		s := stateDeleteTestServer(t, store)
		call := func(method, path string, body any) *httptest.ResponseRecorder {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(raw)))
			return w
		}
		base := "/v1/topologies/lab/gc"
		before := call("GET", base+"/references", nil)
		require.Equal(t, 200, before.Code, before.Body.String())
		var refs persistence.GCReferenceSnapshot
		require.NoError(t, json.Unmarshal(before.Body.Bytes(), &refs))
		require.Equal(t, 400, call("POST", base+"/sources/absent/begin", map[string]any{"topology_uid": refs.TopologyUID}).Code)
		begin := map[string]any{"topology_uid": refs.TopologyUID, "expected_version": 0}
		first := call("POST", base+"/sources/remote/begin", begin)
		require.Equal(t, 200, first.Code, first.Body.String())
		require.Equal(t, 409, call("GET", base+"/references", nil).Code)
		require.Equal(t, 409, call("POST", base+"/sources/remote/begin", begin).Code)
		complete := map[string]any{"topology_uid": refs.TopologyUID, "expected_version": 1, "manifest": persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}}
		done := call("POST", base+"/sources/remote/complete", complete)
		require.Equal(t, 200, done.Code, done.Body.String())
		require.Equal(t, 200, call("POST", base+"/sources/remote/complete", complete).Code)
		require.Equal(t, 200, call("GET", base+"/references", nil).Code)
		list := call("GET", base+"/sources", nil)
		require.Equal(t, 200, list.Code)
		require.Contains(t, list.Body.String(), "remote")
		begin["unexpected"] = true
		require.Equal(t, 400, call("POST", base+"/sources/remote/begin", begin).Code)
	})
}

func TestGCSourceHTTPRetirementFencesFuturePublication(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		refs, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		v, err := repo.BeginGCSource(ctx, "lab", "remote", refs.TopologyUID, 0)
		require.NoError(t, err)
		m := persistence.GCSourceManifest{Domains: map[string][]string{"revision": {"retained"}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "remote", refs.TopologyUID, v, m))
		raw, err := json.Marshal(map[string]any{"topology_uid": refs.TopologyUID, "expected_version": 2})
		require.NoError(t, err)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/topologies/lab/gc/sources/remote/retire", bytes.NewReader(raw)))
		require.Equal(t, 200, w.Code, w.Body.String())
		sources, err := repo.LoadGCSources(ctx, "lab")
		require.NoError(t, err)
		require.True(t, sources["remote"].Retired)
		_, err = repo.BeginGCSource(ctx, "lab", "remote", refs.TopologyUID, 3)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}
