package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func keyedApply(t *testing.T, s *Server, rev, key, value string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"revision": rev, "inputs": map[string]string{"flag": value}})
	q := httptest.NewRequest("POST", "/v1/topologies/keyed/apply", bytes.NewReader(b))
	q.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, q)
	return w
}
func TestKeyedApplyTerminalReplayAndConflict(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	w := keyedApply(t, s, rev, "apply-1", "private-canary")
	require.Equal(t, 202, w.Code, w.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	id := out["run_id"].(string)
	run, _ := s.jobs.get(id)
	s.jobs.finish(run, nil)
	w = keyedApply(t, s, rev, "apply-1", "private-canary")
	require.Equal(t, 202, w.Code, w.Body.String())
	got, _ := s.jobs.get(id)
	require.Equal(t, controlplane.RunDone, got.Status)
	require.Equal(t, http.StatusConflict, keyedApply(t, s, rev, "apply-1", "changed").Code)
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}
func TestKeyedApplyRestartRequiresHydrationAndPreservesReceipt(t *testing.T) {
	work, runs := t.TempDir(), t.TempDir()
	s := NewServer(work, runs)
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	w := keyedApply(t, s, rev, "apply-restart", "private-canary")
	require.Equal(t, 202, w.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	id := out["run_id"].(string)
	restart := NewServer(work, runs)
	_, err := restart.jobs.claim(id, "host-a")
	require.ErrorIs(t, err, errApplyInputsRequired)
	w = keyedApply(t, restart, rev, "apply-restart", "private-canary")
	require.Equal(t, 202, w.Code, w.Body.String())
	claimed, err := restart.jobs.claim(id, "host-a")
	require.NoError(t, err)
	require.Equal(t, "private-canary", claimed.Inputs["flag"])
	durable, err := restart.apiStore.GetRun(context.Background(), id)
	require.NoError(t, err)
	raw, _ := json.Marshal(durable)
	require.NotContains(t, string(raw), "private-canary")
	commands, err := restart.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	raw, _ = json.Marshal(commands)
	require.NotContains(t, string(raw), "private-canary")
}
func TestKeyedDestroyAbsentTopologyHasDurableReceipt(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	req := httptest.NewRequest("POST", "/v1/topologies/never-created/destroy", nil)
	req.Header.Set("Idempotency-Key", "absent-1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, 202, w.Code, w.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	run, err := s.apiStore.GetRun(context.Background(), out["run_id"].(string))
	require.NoError(t, err)
	require.Equal(t, controlplane.RunDone, run.Status)
}
func TestKeyedApplyAtomicAcrossPostgresServers(t *testing.T) {
	dsn := isolatedPostgresTestDSN(t)
	dir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = dsn
	servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
	registerDockerAgent(t, servers[0])
	rev := publishRevision(t, servers[0], upsertApplyHCL)
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses <- keyedApply(t, servers[i%2], rev, "pg-key", "private-canary")
		}(i)
	}
	wg.Wait()
	close(responses)
	id := ""
	for w := range responses {
		require.Equal(t, 202, w.Code, w.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		if id == "" {
			id = out["run_id"].(string)
		}
		require.Equal(t, id, out["run_id"])
	}
	runs, err := servers[0].apiStore.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	commands, err := servers[0].apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	raw, _ := json.Marshal(runs)
	require.NotContains(t, string(raw), "private-canary")
	raw, _ = json.Marshal(commands)
	require.NotContains(t, string(raw), "private-canary")
	restart := NewServerWithConfig(cfg)
	_, err = restart.jobs.claim(id, "host-a")
	require.ErrorIs(t, err, errApplyInputsRequired)
	require.Equal(t, 202, keyedApply(t, restart, rev, "pg-key", "private-canary").Code)
	claimed, err := restart.jobs.claim(id, "host-a")
	require.NoError(t, err)
	require.Equal(t, "private-canary", claimed.Inputs["flag"])
	restart.jobs.finish(claimed, nil)
	require.Equal(t, 202, keyedApply(t, restart, rev, "pg-key", "private-canary").Code)
	finished, err := restart.apiStore.GetRun(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunDone, finished.Status)
}
func TestAbsentDestroyInspectionDeleteAndArchiveReceipt(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	for _, key := range []string{"suspend-1", "archive-2"} {
		q := httptest.NewRequest("POST", "/v1/topologies/never-created/destroy", nil)
		q.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, q)
		require.Equal(t, 202, w.Code, w.Body.String())
		w = httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/topologies/never-created/cleanup-inspection", nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		var evidence struct {
			Resources       []any `json:"resources"`
			Active          []any `json:"active_operations"`
			State, Metadata bool
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &evidence))
		require.Empty(t, evidence.Resources)
		require.Empty(t, evidence.Active)
		w = httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("DELETE", "/v1/topologies/never-created", nil))
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, commands)
}

type failingRunReadStore struct{ apiStore }

func (s failingRunReadStore) GetRun(context.Context, string) (*controlplane.Run, error) {
	return nil, errors.New("backend unavailable")
}
func TestGetRunBackendFailureIs503EvenWithCache(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.apiStore = failingRunReadStore{s.apiStore}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/missing", nil))
	require.Equal(t, 503, w.Code)
}
func TestKeyedApplyConflictingActiveOperationRejected(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	require.Equal(t, 202, keyedApply(t, s, rev, "first", "canary").Code)
	require.Equal(t, 409, keyedApply(t, s, rev, "second", "canary").Code)
	q := httptest.NewRequest("POST", "/v1/topologies/keyed/destroy", nil)
	q.Header.Set("Idempotency-Key", "destroy-other")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, q)
	require.Equal(t, 409, w.Code)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("DELETE", "/v1/topologies/keyed", nil))
	require.Equal(t, 409, w.Code)
}
func TestPostgresAbsentDestroyReceiptsAndInspection(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	dir := t.TempDir()
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = isolatedPostgresTestDSN(t)
	s := NewServerWithConfig(cfg)
	ids := map[string]bool{}
	for _, key := range []string{"suspend-empty", "archive-empty"} {
		q := httptest.NewRequest("POST", "/v1/topologies/absent/destroy", nil)
		q.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, q)
		require.Equal(t, 202, w.Code, w.Body.String())
		var out map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		ids[out["run_id"]] = true
		run, err := s.apiStore.GetRun(context.Background(), out["run_id"])
		require.NoError(t, err)
		require.Equal(t, controlplane.RunDone, run.Status)
		w = httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/topologies/absent/cleanup-inspection", nil))
		require.Equal(t, 200, w.Code)
		var proof struct {
			Resources []any `json:"resources"`
			Active    []any `json:"active_operations"`
			State     bool  `json:"state_exists"`
			Metadata  bool  `json:"metadata_exists"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &proof))
		require.Empty(t, proof.Resources)
		require.Empty(t, proof.Active)
		require.False(t, proof.State)
		require.False(t, proof.Metadata)
	}
	require.Len(t, ids, 2)
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, commands)
}

type failingInspectionStore struct{ apiStore }

func (s failingInspectionStore) LoadRuns(context.Context) ([]controlplane.Run, error) {
	return nil, errors.New("unavailable")
}
func TestCleanupInspectionFailsClosedOnOperationStoreError(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.apiStore = failingInspectionStore{s.apiStore}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/topologies/absent/cleanup-inspection", nil))
	require.Equal(t, 503, w.Code)
}
