package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestCleanupInspectionMissingStateNeverProvesExternalAbsence(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "unknown", out["external_presence"])
	require.Equal(t, false, out["state_exists"])
	require.Equal(t, "local_repository", out["scope"])
	require.Equal(t, "lifecycle_repository", out["source"])
	require.NotEmpty(t, out["inspected_at"])
	require.Empty(t, out["resources"])
	require.Equal(t, []any{"run", "node_operation", "guest_execution", "guest_file", "execution_grant", "console_session", "agent_command"}, out["active_operation_kinds"])
}

type failedInspectionOperations struct{ controlstore.Store }

func (s failedInspectionOperations) LoadRuns(context.Context) ([]controlplane.Run, error) {
	return nil, errors.New("private-dsn")
}

func TestCleanupInspectionRedactsResourcesAndReadsDurableOperations(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", OwnerUID: st.TopologyUID, ResourceUID: "web-uid", Generation: 2, Status: state.ResourcePresent, Attributes: state.Attributes{"secret": "private-attribute"}, Private: json.RawMessage(`{"secret":"private-provider"}`)}}
	require.NoError(t, mgr.Save(st))
	op := controlplane.NodeOperation{ID: "node-op", Workspace: "project-workspace", Topology: "lab", Operation: "pause", AgentID: "agent", Status: controlplane.NodeOperationStatusQueued, CreatedAt: time.Now().UTC(), CommandID: "command", ActionID: "action", ResourceUID: "web-uid", Generation: 2, LeaseEpoch: 1}
	require.NoError(t, s.apiStore.CreateNodeOperation(ctx, op))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 200, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out["resources"], 1)
	require.Len(t, out["active_operations"], 1)
	require.Equal(t, "owning_agent_not_observed", out["scope"])
	require.NotContains(t, rec.Body.String(), "private-attribute")
	require.NotContains(t, rec.Body.String(), "private-provider")
	finished := op
	finished.Status = controlplane.NodeOperationStatusDone
	require.NoError(t, s.apiStore.CompareAndSwapNodeOperation(ctx, op, finished))
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 200, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Empty(t, out["active_operations"])
	s.apiStore = failedInspectionOperations{s.apiStore}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 503, rec.Code)
	require.NotContains(t, rec.Body.String(), "private-dsn")
}

func TestCleanupInspectionIncludesUncommittedCreateAndRemoteScope(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.CommitActionIntent(ctx, runtime.ActionRecord{ID: "pending", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "uid", Generation: 1, Error: "private-error-secret"}))
	require.NoError(t, s.workspaceService().BindPlacement("lab", "owner-agent", controlplane.AgentProtocolVersion))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 200, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "owning_agent_not_observed", out["scope"])
	require.Equal(t, "owner-agent", out["agent_id"])
	require.Equal(t, "unknown", out["external_presence"])
	require.Len(t, out["unfinished_actions"], 1)
	require.Empty(t, out["resources"])
	require.NotContains(t, rec.Body.String(), "private-error-secret")
	require.Contains(t, rec.Body.String(), `"resource_uid":"uid"`)
}

func TestCleanupInspectionUnavailableAndAuthenticated(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(s.runsDir, "lab"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(s.runsDir, "lab", "state.json"), []byte(`{"version":1}`), 0600))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 503, rec.Code)
	s.cfg.API.Token = "secret"
	rec = httptest.NewRecorder()
	authMiddleware(s).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 401, rec.Code)
}
