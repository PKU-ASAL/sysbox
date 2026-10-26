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

func TestCapabilitiesDistinguishSharedAuthorityFromLocalStores(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		server := stateDeleteTestServer(t, store)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest("GET", "/v1/capabilities", nil))
		require.Equal(t, 200, response.Code)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fields))
		_, shared := store.(*postgresAPIStore)
		require.Equal(t, shared, fields["multi_api_execution"])
		if shared {
			require.Equal(t, "shared-postgres", fields["execution_store_scope"])
		} else {
			require.Equal(t, "host-local", fields["execution_store_scope"])
		}
		require.Equal(t, float64(1), fields["recovery_inputs_protocol"])
		require.Equal(t, false, fields["external_absence_inspection"])
		require.Equal(t, false, fields["resource_lease_revocation"])
	})
}
