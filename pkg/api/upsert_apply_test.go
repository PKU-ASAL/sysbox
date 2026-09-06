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
	"path/filepath"
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

// applyOperationKey must be deterministic across map iteration order and
// injective: distinct (revision, inputs, allow_unsafe_state) tuples must not
// collide, even when an input value embeds field delimiters.
func TestApplyOperationKeyCanonicalAndInjective(t *testing.T) {
	rev := "sha256:abc123"

	// Same inputs in a different map insertion order hash identically.
	require.Equal(t,
		applyOperationKey(rev, map[string]string{"a": "1", "b": "2"}, false),
		applyOperationKey(rev, map[string]string{"b": "2", "a": "1"}, false),
	)

	// A value that embeds a newline + a synthetic next-field must not collide
	// with the genuinely-two-input map.
	require.NotEqual(t,
		applyOperationKey(rev, map[string]string{"a": "1\ninput:b=2"}, false),
		applyOperationKey(rev, map[string]string{"a": "1", "b": "2"}, false),
	)

	// allow_unsafe_state participates in the key.
	require.NotEqual(t,
		applyOperationKey(rev, map[string]string{"a": "1"}, true),
		applyOperationKey(rev, map[string]string{"a": "1"}, false),
	)
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

// The apply response must carry the topology name and its projected status in
// addition to the run/agent identifiers, so a consumer can read the convergence
// status directly without an extra GET.
func TestApplyUpsertResponseIncludesNameAndStatus(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	const topology = "cf-upsert-a"
	rev := publishRevision(t, s, upsertApplyHCL)

	body, err := json.Marshal(map[string]any{"revision": rev, "inputs": map[string]string{}})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/"+topology+"/apply", bytes.NewBuffer(body)))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var applied struct {
		Name   string                       `json:"name"`
		Status *controlplane.TopologyStatus `json:"status"`
		RunID  string                       `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &applied))
	require.Equal(t, topology, applied.Name)
	require.NotNil(t, applied.Status)
	require.NotEmpty(t, applied.RunID)
	require.Equal(t, controlplane.PhaseConverging, applied.Status.Phase)
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
// runs coalesce, so a new apply must replace the terminal record with a fresh
// in-flight run under the same deterministic id, and a third apply must coalesce
// with that fresh run rather than the stale terminal record.
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
	require.Equal(t, first, second, "the retry must reuse the deterministic run id")

	secondRun, ok := s.jobs.get(second)
	require.True(t, ok)
	require.Equal(t, rev, secondRun.Revision)
	require.True(t, secondRun.Status.IsActive(), "the retry must be a fresh in-flight run, not the terminal record")
	require.Empty(t, secondRun.Err, "the fresh run must not carry the terminal error")

	third := applyUpsert(t, s, "cf-upsert-a", rev)
	require.Equal(t, second, third, "a third apply must coalesce with the in-flight retry, not spawn a duplicate")

	// The retried run reuses the deterministic id, so its log broadcaster must
	// be freshly opened, not the one closed by the terminal attempt.
	ch := s.jobs.logs.Writer(second).Subscribe()
	select {
	case _, open := <-ch:
		// A fresh, open broadcaster has no buffered lines yet, so an immediate
		// receive means the broadcaster was already closed (open == false).
		require.True(t, open, "retried run log broadcaster must be open, not closed")
	default:
		// No immediate receive: the broadcaster is still open.
	}
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

// Applying a global revision must materialize the whole project directory tree
// (root HCL + modules/ + files/), not just the single root HCL.
func TestApplyUpsertMaterializesDirectoryTree(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	files := map[string]string{
		"field.sysbox.hcl": `resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}`,
		"modules/web/main.hcl": `resource "sysbox_node" "web" {}`,
		"files/run.txt":        "hello world",
	}
	rev := publishRevisionFiles(t, s, files)
	applyUpsert(t, s, "cf-tree", rev)

	base := filepath.Dir(s.workspaceService().HCLFile("cf-tree"))
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(p)))
		require.NoError(t, err)
		require.Equal(t, want, string(got), "content mismatch at %s", p)
	}
}

// Re-applying a different revision must reconcile the tree: a file present in
// the old revision but absent from the new one must be removed, not left as a
// stale union of the two revisions.
func TestApplyUpsertReconcilesRemovedFiles(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	revA := publishRevisionFiles(t, s, map[string]string{
		"field.sysbox.hcl":     upsertApplyHCL,
		"modules/web/main.hcl": `resource "sysbox_node" "web" {}`,
		"files/extra.txt":      "extra",
	})
	applyUpsert(t, s, "cf-reconcile", revA)

	base := filepath.Dir(s.workspaceService().HCLFile("cf-reconcile"))
	_, err := os.Stat(filepath.Join(base, "files", "extra.txt"))
	require.NoError(t, err, "revision A must materialize files/extra.txt")

	revB := publishRevisionFiles(t, s, map[string]string{"field.sysbox.hcl": upsertApplyHCL})
	applyUpsert(t, s, "cf-reconcile", revB)

	for _, stale := range []string{"files/extra.txt", "modules/web/main.hcl"} {
		_, err := os.Stat(filepath.Join(base, filepath.FromSlash(stale)))
		require.True(t, os.IsNotExist(err), "%s removed from the new revision must be gone", stale)
	}

	got, err := os.ReadFile(filepath.Join(base, "field.sysbox.hcl"))
	require.NoError(t, err)
	require.Equal(t, upsertApplyHCL, string(got))
}
