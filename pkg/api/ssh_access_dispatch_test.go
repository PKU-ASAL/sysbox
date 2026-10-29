package api

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSHAccessEndpointRejectsMalformedRequestBeforeDispatch(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/topologies/lab/ssh-access", bytes.NewBufferString(`{"operation":"ssh_access_apply","node":"web","id":"access","access_id":"admin","topology":"lab","authorized_keys":["ssh-private-key secret"]}`)))
	require.Equal(t, 400, r.Code, r.Body.String())
}
