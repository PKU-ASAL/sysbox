package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func resourceAddressForTest() address.Address { return address.Resource("sysbox_node", "old") }
func TestHistoricalIntentInspectionIsReadOnly(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	a := runtime.ActionRecord{ID: "old", Topology: "lab", RunID: "run", Resource: resourceAddressForTest(), Action: "create", Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "resource", Generation: 1, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Revision: 0, ProviderOperationID: "old", Quiescence: "unknown"}
	require.NoError(t, repo.CommitActionIntent(context.Background(), a))
	st, err = repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	before, err := repo.LoadActions(context.Background(), "lab")
	require.NoError(t, err)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/topologies/lab/recovery/intent-only", nil))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "manual_reconciliation_required")
	after, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, st, after)
	actions, err := repo.LoadActions(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, before, actions)
}

func TestHistoricalIntentInspectionDoesNotCreateMissingLifecycle(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	repo, err := s.actionRepository("missing")
	require.NoError(t, err)
	before, err := repo.ListTopologies(context.Background())
	require.NoError(t, err)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/topologies/missing/recovery/intent-only", nil))
	require.Equal(t, 200, w.Code)
	after, err := repo.ListTopologies(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after)
}
