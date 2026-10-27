package api

import (
	"bytes"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestAdoptionPlanIsReadOnlyAndFailClosedWithoutCapability(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	require.NoError(t, mgr.Save(&state.State{Version: state.SchemaVersion, Lineage: "topology"}))
	before, err := s.stateManager("lab")
	require.NoError(t, err)
	stateBefore, err := before.Load()
	require.NoError(t, err)
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/topologies/lab/adoption-plan", bytes.NewBufferString(`{"type":"sysbox_node","name":"web","id":"external","substrate":"missing"}`)))
	require.Equal(t, 200, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), `"decision":"unsupported"`)
	after, err := before.Load()
	require.NoError(t, err)
	require.Equal(t, stateBefore, after)
}
