package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOldEndpointsRemoved(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	// Method-aware routing: a removed method on a path that still has another
	// method returns 405, a fully-removed path returns 404. Both prove the old
	// handler no longer serves the request.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/topologies"},
		{http.MethodPut, "/v1/topologies/lab/hcl"},
		{http.MethodPost, "/v1/topologies/lab/plans"},
		{http.MethodPost, "/v1/topologies/lab/revisions"},
		{http.MethodGet, "/v1/topologies/lab/revisions"},
		{http.MethodGet, "/v1/topologies/lab/revisions/abc123"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(""))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Contains(t,
			[]int{http.StatusNotFound, http.StatusMethodNotAllowed},
			rec.Code,
			"%s %s should be removed", tc.method, tc.path,
		)
	}
}
