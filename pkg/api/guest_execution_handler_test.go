package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

func guestExecutionTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	s := NewServerWithConfig(cfg)
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	require.NoError(t, mgr.Save(&state.State{
		Version:   state.SchemaVersion,
		Resources: []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node"}},
	}))
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	require.NoError(t, s.apiStore.SaveAgentInventory(context.Background(), controlplane.AgentInventory{AgentID: "host-a", Topologies: []controlplane.InventoryItem{{Topology: "lab", Available: true}}}))
	return s
}

func TestGuestExecutionHTTPCreateGetCancel(t *testing.T) {
	s := guestExecutionTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/nodes/web/executions", bytes.NewBufferString(`{"argv":["true"]}`))
	req.Header.Set("X-Sysbox-Roles", "admin")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var created controlplane.GuestExecution
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Equal(t, controlplane.GuestExecutionQueued, created.Status)
	require.NotEmpty(t, created.ID)
	require.NotContains(t, rec.Body.String(), "agent_id")

	rec = httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "/v1/executions/"+created.ID, nil)
	get.Header.Set("X-Sysbox-Roles", "admin")
	s.ServeHTTP(rec, get)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	cancel := httptest.NewRequest(http.MethodPost, "/v1/executions/"+created.ID+"/cancel", nil)
	cancel.Header.Set("X-Sysbox-Roles", "admin")
	s.ServeHTTP(rec, cancel)
	require.Equal(t, http.StatusAccepted, rec.Code)
}

func TestGuestExecutionHTTPRejectsInvalidDeniedAndUnknown(t *testing.T) {
	s := guestExecutionTestServer(t)
	for _, tc := range []struct {
		path, body, roles string
		want              int
	}{
		{"/v1/topologies/lab/nodes/web/executions", `{"argv":[]}`, "admin", http.StatusBadRequest},
		{"/v1/topologies/lab/nodes/web/executions", `{"argv":["true"]}`, "viewer", http.StatusForbidden},
		{"/v1/topologies/missing/nodes/web/executions", `{"argv":["true"]}`, "admin", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("X-Sysbox-Roles", tc.roles)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Equal(t, tc.want, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "host-a")
	}
}
