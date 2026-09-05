package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func condition(t *testing.T, s *controlplane.TopologyStatus, typ string) controlplane.Condition {
	t.Helper()
	for _, c := range s.Conditions {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("condition %q not found in %+v", typ, s.Conditions)
	return controlplane.Condition{}
}

func getTopology(t *testing.T, s *Server, name string) WorkspaceInfo {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/"+name, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body WorkspaceInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// createWorkspace writes an HCL file so the topology exists as a workspace.
func createWorkspace(t *testing.T, s *Server, name string) {
	t.Helper()
	hclPath := s.workspaceService().HCLFile(name)
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`resource "sysbox_network" "lab" { cidr = "10.0.0.0/24" }`), 0o644))
}

// A topology whose apply completed and whose resources are healthy reports its
// readiness conditions: Applied and Provisioned are True, but the assertion has
// not been evaluated, so Asserted and Ready are Unknown — not False.
func TestGetTopologyReportsReadinessConditions(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab")

	// A completed apply run.
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunDone
	s.jobs.replace(run)

	// A healthy observation.
	require.NoError(t, s.saveHealthSnapshot("lab", HealthSnapshot{
		Topology: "lab",
		Health:   controlplane.TopologyHealth{Status: controlplane.ResourceHealthHealthy},
	}))

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status, "GET /v1/topologies/{t} must report a status block")
	require.Equal(t, controlplane.ConditionTrue, condition(t, body.Status, controlplane.ConditionApplied).Status)
	require.Equal(t, controlplane.ConditionTrue, condition(t, body.Status, controlplane.ConditionProvisioned).Status)
	require.Equal(t, controlplane.ConditionUnknown, condition(t, body.Status, controlplane.ConditionAsserted).Status)
	require.Equal(t, controlplane.ConditionUnknown, condition(t, body.Status, controlplane.ConditionReady).Status,
		"unevaluated assertion yields Unknown Ready, not False")
	require.Equal(t, controlplane.PhaseConverging, body.Status.Phase)
}

// A failed apply surfaces as Applied False and Ready False with the failing
// condition named.
func TestGetTopologyReportsFailedApply(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab")

	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunFailed
	run.Err = "create sysbox_node.web: no such image"
	s.jobs.replace(run)

	body := getTopology(t, s, "lab")

	require.Equal(t, controlplane.ConditionFalse, condition(t, body.Status, controlplane.ConditionApplied).Status)
	require.Equal(t, controlplane.ConditionFalse, condition(t, body.Status, controlplane.ConditionReady).Status)
	require.Equal(t, "AppliedFailed", condition(t, body.Status, controlplane.ConditionReady).Reason)
	require.Equal(t, controlplane.PhaseFailed, body.Status.Phase)
}

// A topology with no run and no observation reports everything Unknown.
func TestGetTopologyReportsUnknownWhenNeverObserved(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	for _, c := range body.Status.Conditions {
		require.Equal(t, controlplane.ConditionUnknown, c.Status, "condition %q", c.Type)
	}
	require.Equal(t, controlplane.PhaseGone, body.Status.Phase,
		"a topology with no HCL and no state is gone")
}

// A converging run reports its deadline_at: the instant by which the system
// will declare the topology failed if it has not converged.
func TestGetTopologyReportsConvergenceDeadline(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab")

	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunRunning
	s.jobs.replace(run)

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.NotNil(t, body.Status.DeadlineAt, "a converging topology must report its deadline")
	require.True(t, body.Status.DeadlineAt.After(run.StartedAt))
	require.Equal(t, controlplane.PhaseConverging, body.Status.Phase)
}

// A converging run carrying an explicit per-apply deadline_at reports that
// instant, not the config-default StartedAt + timeout projection, so the
// reported deadline matches the instant the supervisor will actually fail it.
func TestGetTopologyReportsPerRunDeadline(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab")

	deadline := time.Now().Add(-1 * time.Minute) // already past
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunRunning
	run.StartedAt = time.Now().Add(-10 * time.Minute)
	run.DeadlineAt = deadline
	s.jobs.replace(run)

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.NotNil(t, body.Status.DeadlineAt)
	require.True(t, body.Status.DeadlineAt.Equal(deadline),
		"reported deadline %v, want per-run deadline %v", body.Status.DeadlineAt, deadline)
}

// A run that evaluated a failing assertion surfaces it as Asserted False with
// the author's message and the failing check names.
func TestGetTopologyReportsFailedAssertion(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab")

	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunDone
	run.Assertion = &controlplane.AssertionResult{
		Message:      "edge 不应能连到 core:5432，但连上了",
		FailedChecks: []string{"edge_cannot_reach_core"},
	}
	s.jobs.replace(run)

	body := getTopology(t, s, "lab")

	require.Equal(t, controlplane.ConditionFalse, condition(t, body.Status, controlplane.ConditionAsserted).Status)
	require.Equal(t, "edge 不应能连到 core:5432，但连上了", condition(t, body.Status, controlplane.ConditionAsserted).Message)
	require.Equal(t, controlplane.PhaseFailed, body.Status.Phase)
}

// A run whose checks all passed surfaces Asserted True (not Unknown): it was
// evaluated, and it held.
func TestGetTopologyReportsPassedAssertion(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab")

	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunDone
	run.Assertion = &controlplane.AssertionResult{}
	s.jobs.replace(run)

	body := getTopology(t, s, "lab")

	require.Equal(t, controlplane.ConditionTrue, condition(t, body.Status, controlplane.ConditionAsserted).Status)
}
