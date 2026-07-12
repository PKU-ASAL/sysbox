package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
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
		Resources []controlplane.ResourceHealth `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Resources, 1)
	require.Equal(t, "sysbox_kernel.linux", body.Resources[0].Resource)
	require.Equal(t, controlplane.ResourceHealthHealthy, body.Resources[0].Status)
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
	body := bytes.NewBufferString(`{
  "agent_id": "host-a",
  "topology": "mixed",
  "workspace": "mixed",
  "observed_at": "2026-05-29T06:52:52Z",
  "resources": [
    {"resource":"test_resource.web","type":"test_resource","name":"web","provider":"test","status":"drifted","reason":"exited","decision":"mark_drift"}
  ]
}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/projections/resources", body))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "projections")

	var out struct {
		Resources []controlplane.ResourceHealth `json:"resources"`
		Health    controlplane.TopologyHealth   `json:"health"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Resources, 1)
	require.Equal(t, controlplane.ResourceHealthDrifted, out.Resources[0].Status)
	require.Equal(t, controlplane.ResourceHealthDrifted, out.Health.Status)
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
	var body controlplane.ResourceHealth
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "sysbox_kernel.linux", body.Resource)
	require.Equal(t, controlplane.ResourceHealthDrifted, body.Status)
	require.Equal(t, controlplane.RecoveryDecisionMarkDrift, body.Decision)
}

func writeState(t *testing.T, runsDir, topology string, st *state.State) {
	t.Helper()
	mgr := state.NewManager(filepath.Join(runsDir, topology, "state.json"))
	require.NoError(t, mgr.Save(st))
}

type testResourceProvider struct{}

func (testResourceProvider) Type() string { return "test_resource" }
func (testResourceProvider) Schema() runtime.ResourceSchema {
	return runtime.ResourceSchemaFor("test_resource")
}
func (testResourceProvider) Read(context.Context, state.Resource) (runtime.ResourceReadResult, error) {
	return runtime.ResourceReadResult{Decision: controlplane.RecoveryDecisionNoop}, nil
}
func (testResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	return controlplane.PlanAction{Resource: desired.Address.String(), Type: desired.Address.Type, Name: desired.Address.Name, Action: controlplane.PlanActionNoop}, nil
}
func (testResourceProvider) Create(context.Context, *runtime.ProviderContext, *graph.Node) (state.Resource, error) {
	return state.Resource{}, nil
}
func (testResourceProvider) Update(context.Context, *runtime.ProviderContext, *graph.Node, state.Resource) (state.Resource, error) {
	return state.Resource{}, nil
}
func (testResourceProvider) Delete(context.Context, *runtime.ProviderContext, state.Resource) error {
	return nil
}
func (testResourceProvider) ExternalID(state.Resource) string { return "" }

func init() {
	runtime.RegisterResourceProvider(testResourceProvider{})
}
