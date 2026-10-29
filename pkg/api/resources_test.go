package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestListResources(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	kernel := filepath.Join(dir, "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel"), 0o644))
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": kernel},
		}},
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Resources []model.ResourceHealth `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Resources, 1)
	require.Equal(t, "sysbox_kernel.linux", body.Resources[0].Resource)
	require.Equal(t, model.ResourceHealthHealthy, body.Resources[0].Status)
}

func TestListResourcesUsesLatestAgentProjectionAsAuthoritative(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("test_resource", "web"),
			Driver:     "test",
			Attributes: map[string]any{},
		}},
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	session, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).OpenSession(context.Background(), "host-a", "resources-process", "")
	require.NoError(t, err)
	body := bytes.NewBufferString(`{
  "agent_id": "host-a",
	  "session_id": "` + session.ID + `",
	  "sequence": 1,
  "topology": "mixed",
  "workspace": "mixed",

  "resources": [
    {"resource":"test_resource.web","type":"test_resource","name":"web","provider":"test","status":"drifted","reason":"exited","decision":"mark_drift"}
  ]
}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/observations", bindObservationTestBody(t, s, "host-a", body)))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "projections")

	var out struct {
		Resources []model.ResourceHealth `json:"resources"`
		Health    model.TopologyHealth   `json:"health"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Resources, 1)
	require.Equal(t, model.ResourceHealthDrifted, out.Resources[0].Status)
	require.Equal(t, model.ResourceHealthDrifted, out.Health.Status)
	require.Equal(t, 1, out.Health.Drifted)
}

func TestGetResourceHealth(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	kernel := filepath.Join(dir, "missing")
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": kernel},
		}},
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources/sysbox_kernel.linux/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body model.ResourceHealth
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "sysbox_kernel.linux", body.Resource)
	require.Equal(t, model.ResourceHealthDrifted, body.Status)
	require.Equal(t, model.RecoveryDecisionMarkDrift, body.Decision)
}

func writeState(t *testing.T, runsDir, topology string, st *state.State) {
	t.Helper()
	repo, err := persistence.OpenFilesystem(filepath.Join(runsDir, topology, "state.json.lifecycle"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repo.Close()) })
	current, err := repo.LoadState(context.Background(), topology)
	require.NoError(t, err)
	st.TopologyUID = current.TopologyUID
	st.Meta = current.Meta
	for i := range st.Resources {
		st.Resources[i].OwnerUID = st.TopologyUID
		st.Resources[i].ResourceUID = st.TopologyUID + "/" + st.Resources[i].Address.String()
		if st.Resources[i].Generation == 0 {
			st.Resources[i].Generation = 1
		}
	}
	mgr := state.NewManagerWithBackend(repo.StateBackend(topology))
	require.NoError(t, mgr.Save(st))
}

type testResourceHandler struct{}

func (testResourceHandler) Type() string { return "test_resource" }
func (testResourceHandler) Schema() runtime.ResourceSchema {
	return runtime.ResourceSchemaFor("test_resource")
}
func (testResourceHandler) Read(context.Context, state.Resource, *driver.Registry) (runtime.ResourceReadResult, error) {
	return runtime.ResourceReadResult{Decision: model.RecoveryDecisionNoop}, nil
}
func (testResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	return model.PlannedChange{Address: desired.Address, Action: model.PlanActionNoop}, nil
}
func (testResourceHandler) Create(context.Context, *runtime.ProviderContext, *graph.Node) (state.Resource, error) {
	return state.Resource{}, nil
}
func (testResourceHandler) Delete(context.Context, *runtime.ProviderContext, state.Resource) error {
	return nil
}
func (testResourceHandler) ExternalID(state.Resource) string { return "" }

func init() {
	runtime.RegisterResourceHandler(testResourceHandler{})
}
