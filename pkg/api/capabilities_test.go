package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilitiesDescribeSysboxEvidenceBoundaries(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/capabilities", nil))
	require.Equal(t, 200, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "sysbox-runtime-v1", out["protocol"])
	require.Equal(t, true, out["cleanup_inspection"])
	require.Equal(t, true, out["durable_apply"])
	require.Equal(t, true, out["keyed_destroy"])
	require.Equal(t, false, out["external_absence_inspection"])
	require.Equal(t, false, out["resource_lease_revocation"])
	require.NotContains(t, rec.Body.String(), "cyberfield")
	require.Contains(t, out, "checks")
}
