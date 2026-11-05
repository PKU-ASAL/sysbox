package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestCapabilitiesAdvertiseResultContract(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
	require.Equal(t, http.StatusOK, r.Code)
	var result struct {
		Cleanup int `json:"cleanup_receipt_protocol"`
		Errors  int `json:"run_error_protocol"`
	}
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &result))
	require.Equal(t, 1, result.Cleanup)
	require.Equal(t, 1, result.Errors)
}

func TestSignedHostErrorCodeSurvivesRunQuery(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		run := claimedReceiptRun(t, store, "result-code")
		server := stateDeleteTestServer(t, store)
		fixture := prepareHostRunFixture(t, server, *run, "topology-uid")
		result := *run
		result.MarkFinished(errors.New("provider read failed"), time.Now().UTC())
		result.ErrorCode = "observation_unknown"
		stored := fixture.complete(t, result)
		require.Equal(t, "observation_unknown", stored.ErrorCode)
		out := httptest.NewRecorder()
		server.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID, nil))
		require.Equal(t, http.StatusOK, out.Code, out.Body.String())
		var wire struct {
			ErrorCode string `json:"error_code"`
		}
		require.NoError(t, json.Unmarshal(out.Body.Bytes(), &wire))
		require.Equal(t, "observation_unknown", wire.ErrorCode)
	})
}
