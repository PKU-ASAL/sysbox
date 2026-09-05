package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const upsertApplyHCL = `resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}`

func registerDockerAgent(t *testing.T, s *Server) {
	t.Helper()
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       controlplane.AgentStatusOnline,
		Capabilities: []string{"docker"},
	}))
}

func applyUpsert(t *testing.T, s *Server, topology, revision string) string {
	t.Helper()
	body := `{"revision":"` + revision + `","inputs":{}}`
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/"+topology+"/apply", bytes.NewBufferString(body)))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var started struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.NotEmpty(t, started.RunID)
	return started.RunID
}

// Applying a global revision to a topology that does not yet exist must create
// the workspace and materialize the revision's HCL, then dispatch the run.
func TestApplyUpsertCreatesTopologyOnFirstCall(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	runID := applyUpsert(t, s, "cf-upsert-a", rev)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/cf-upsert-a", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var info WorkspaceInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.True(t, info.HasHCL, "apply must materialize the revision HCL into the workspace")
	require.Equal(t, "cf-upsert-a", info.Name)

	data, err := os.ReadFile(s.workspaceService().HCLFile("cf-upsert-a"))
	require.NoError(t, err)
	require.Equal(t, upsertApplyHCL, string(data))

	run, ok := s.jobs.get(runID)
	require.True(t, ok)
	require.Equal(t, rev, run.Revision, "the run must record the global revision digest")
}

// Applying the same revision again must converge, not error: the workspace HCL
// is overwritten in place and a fresh run is dispatched.
func TestApplyUpsertIsIdempotent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	first := applyUpsert(t, s, "cf-upsert-a", rev)
	second := applyUpsert(t, s, "cf-upsert-a", rev)

	require.NotEqual(t, first, second, "each apply dispatches a distinct run")
	secondRun, ok := s.jobs.get(second)
	require.True(t, ok)
	require.Equal(t, rev, secondRun.Revision)

	data, err := os.ReadFile(s.workspaceService().HCLFile("cf-upsert-a"))
	require.NoError(t, err)
	require.Equal(t, upsertApplyHCL, string(data))
}

// deadline_at is carried through to the run record.
func TestApplyUpsertSetsDeadlineAt(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	body, err := json.Marshal(map[string]any{
		"revision":    rev,
		"inputs":      map[string]string{},
		"deadline_at": deadline,
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/cf-upsert-a/apply", bytes.NewBuffer(body)))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var started struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))

	run, ok := s.jobs.get(started.RunID)
	require.True(t, ok)
	require.Equal(t, deadline, run.DeadlineAt)
}
