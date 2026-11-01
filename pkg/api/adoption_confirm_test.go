package api

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdoptionConfirmRejectsMissingEvidenceBeforeDispatch(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/agents/agent/adoption-confirm", bytes.NewBufferString(`{"id":"adopt","topology":"lab","topology_uid":"uid","address":"sysbox_node.web","provider":"docker","external_id":"id","resource_uid":"resource","generation":1,"expected_serial":1}`)))
	require.NotEqual(t, 202, r.Code)
}
