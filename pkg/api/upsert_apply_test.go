package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
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
	return applyUpsertInputs(t, s, topology, revision, map[string]string{})
}

func applyUpsertInputs(t *testing.T, s *Server, topology, revision string, inputs map[string]string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"revision": revision, "inputs": inputs})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/"+topology+"/apply", bytes.NewBuffer(body)))
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

// Applying the same revision+inputs again must coalesce onto the in-flight run,
// not dispatch a second one: the workspace HCL is overwritten in place and the
// same run id is returned.
func TestApplyUpsertIsIdempotent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	first := applyUpsert(t, s, "cf-upsert-a", rev)
	second := applyUpsert(t, s, "cf-upsert-a", rev)

	require.Equal(t, first, second, "concurrent apply with the same revision+inputs must coalesce")
	secondRun, ok := s.jobs.get(second)
	require.True(t, ok)
	require.Equal(t, rev, secondRun.Revision)

	data, err := os.ReadFile(s.workspaceService().HCLFile("cf-upsert-a"))
	require.NoError(t, err)
	require.Equal(t, upsertApplyHCL, string(data))
}

// Same revision but different inputs must produce different runs.
func TestApplyUpsertDifferentInputsGiveDifferentRuns(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	first := applyUpsertInputs(t, s, "cf-upsert-a", rev, map[string]string{"replicas": "1"})
	second := applyUpsertInputs(t, s, "cf-upsert-a", rev, map[string]string{"replicas": "2"})

	require.NotEqual(t, first, second, "different inputs must not coalesce")
}

// A terminal (failed/done) run must not be returned by a retry: only in-flight
// runs coalesce, so a new apply must start a fresh run.
func TestApplyUpsertRetryAfterTerminalCreatesNewRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	first := applyUpsert(t, s, "cf-upsert-a", rev)

	run, ok := s.jobs.get(first)
	require.True(t, ok)
	s.jobs.finish(run, errors.New("boom"))
	require.Equal(t, controlplane.RunFailed, run.Status)

	second := applyUpsert(t, s, "cf-upsert-a", rev)
	require.NotEqual(t, first, second, "a terminal run must not be returned; a fresh run is required")

	secondRun, ok := s.jobs.get(second)
	require.True(t, ok)
	require.Equal(t, rev, secondRun.Revision)
	require.True(t, secondRun.Status.IsActive(), "the fresh retry run must be in-flight")
}

// Concurrent applies with the same revision+inputs must coalesce into exactly
// one run.
func TestApplyUpsertConcurrentCoalesces(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	rev := publishRevision(t, s, upsertApplyHCL)
	body := []byte(`{"revision":"` + rev + `","inputs":{}}`)
	const workers = 32

	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/cf-upsert-a/apply", bytes.NewReader(body)))
			if rec.Code != http.StatusAccepted {
				errs <- fmt.Errorf("status %d: %s", rec.Code, rec.Body.String())
				return
			}
			var response struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				errs <- err
				return
			}
			ids <- response.RunID
		}()
	}
	group.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	var runID string
	for id := range ids {
		if runID == "" {
			runID = id
		}
		require.Equal(t, runID, id, "all concurrent applies must coalesce to one run")
	}
	require.NotEmpty(t, runID)

	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1, "a coalesced apply must dispatch exactly one command")
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

// A legacy workspace revision id (from the old POST /v1/topologies/{t}/revisions
// flow) is not a global digest, so it must still dispatch a run as a label
// without a 404.
func TestApplyLegacyWorkspaceRevisionStillDispatches(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       controlplane.AgentStatusOnline,
		Capabilities: []string{"network"},
	}))

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/apply", bytes.NewBufferString(`{"revision":"abc123"}`)))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var started struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.NotEmpty(t, started.RunID)

	run, ok := s.jobs.get(started.RunID)
	require.True(t, ok)
	require.Equal(t, "abc123", run.Revision)
}
