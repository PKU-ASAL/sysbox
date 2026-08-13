package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestFilePutValidatesPathModeDigestAndStagesPrivately(t *testing.T) {
	s := guestExecutionTestServer(t)
	s.cfg.API.RBAC.AdminRoles = []string{"admin"}
	req := httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path=/opt/challenge/flag&mode=0400", bytes.NewReader([]byte{0, 1, 2}))
	req.Header.Set("X-Sysbox-Roles", "admin")
	req.Header.Set("X-Content-SHA256", "ae4b3280e56e2faf83f414a6e3dabe9d5fbe18976544c05fed121accb85b53fc")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), s.runsDir)
	require.NotContains(t, rec.Body.String(), "fetch_ref")
	require.NotContains(t, rec.Body.String(), "/opt/challenge/flag")

	for _, path := range []string{"relative", "/tmp/../etc/passwd", "/tmp/%00bad"} {
		req = httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path="+path+"&mode=0400", bytes.NewReader([]byte("secret")))
		req.Header.Set("X-Sysbox-Roles", "admin")
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.NotContains(t, rec.Body.String(), "secret")
	}
}
