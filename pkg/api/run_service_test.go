package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestDestroyHTTPIdempotencyKeyDispatchesOnceAcrossConcurrencyAndRestart(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	s := NewServer(configDir, stateDir)
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	const workers = 32
	ids := make(chan string, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
			req.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			require.Equal(t, http.StatusAccepted, rec.Code)
			var response struct {
				RunID string `json:"run_id"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			ids <- response.RunID
		}()
	}
	group.Wait()
	close(ids)
	var runID string
	for id := range ids {
		if runID == "" {
			runID = id
		}
		require.Equal(t, runID, id)
	}
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)

	restarted := NewServer(configDir, stateDir)
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	req.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
	var response struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, runID, response.RunID)
	commands, err = restarted.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}

func TestDestroyHTTPRejectsInvalidIdempotencyKey(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	req.Header.Set("Idempotency-Key", "contains/slash")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDestroyHTTPRejectsIdempotencyKeyReusedWithDifferentUnsafeState(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	first := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	first.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
	firstRecorder := httptest.NewRecorder()
	s.ServeHTTP(firstRecorder, first)
	require.Equal(t, http.StatusAccepted, firstRecorder.Code)

	second := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy?allow_unsafe_state=true", nil)
	second.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
	secondRecorder := httptest.NewRecorder()
	s.ServeHTTP(secondRecorder, second)
	require.Equal(t, http.StatusConflict, secondRecorder.Code)
	require.Contains(t, secondRecorder.Body.String(), "idempotency key")
}

func TestDestroyHTTPIdempotencyReplayDoesNotRequireTopologyMetadata(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	first := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	first.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
	firstRecorder := httptest.NewRecorder()
	s.ServeHTTP(firstRecorder, first)
	require.Equal(t, http.StatusAccepted, firstRecorder.Code, firstRecorder.Body.String())
	var firstResponse struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(firstRecorder.Body.Bytes(), &firstResponse))
	require.NoError(t, os.Remove(s.workspaceService().HCLFile("lab")))

	replay := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	replay.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
	replayRecorder := httptest.NewRecorder()
	s.ServeHTTP(replayRecorder, replay)
	require.Equal(t, http.StatusAccepted, replayRecorder.Code, replayRecorder.Body.String())
	var replayResponse struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(replayRecorder.Body.Bytes(), &replayResponse))
	require.Equal(t, firstResponse.RunID, replayResponse.RunID)
}

func TestDestroyHTTPIdempotencyIsAtomicAcrossSQLiteServers(t *testing.T) {
	dir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = "sqlite://" + filepath.Join(dir, "api.db")
	servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
	writeRunServiceTopology(t, servers[0], "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, servers[0].agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	const workers = 24
	responses := make(chan *httptest.ResponseRecorder, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(server *Server) {
			defer group.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
			req.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			responses <- rec
		}(servers[i%len(servers)])
	}
	group.Wait()
	close(responses)

	var runID string
	for rec := range responses {
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		var response struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		if runID == "" {
			runID = response.RunID
		}
		require.Equal(t, runID, response.RunID)
	}
	runs, err := servers[0].apiStore.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	commands, err := servers[0].apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}

func TestDestroyHTTPIdempotencyAdoptsLegacyRunWithoutRedispatch(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.MustLoadServiceConfig("")
			cfg.Paths.RunsDir = filepath.Join(dir, "runs")
			cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(dir, "api.db")
			} else {
				cfg.State.Backend = ""
			}
			assertLegacyRunDispatchAdopted(t, cfg)
		})
	}
}

func assertLegacyRunDispatchAdopted(t *testing.T, cfg config.ServiceConfig) {
	t.Helper()
	server := NewServerWithConfig(cfg)
	writeRunServiceTopology(t, server, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, server.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))
	const operationKey = "destroy:legacy-adoption"
	legacy := newRun("lab", "destroy", runStartOptions{OperationKey: operationKey})
	legacy.MarkAssigned("host-a", time.Now().Add(-time.Minute).UTC())
	legacy.AssignedAt = legacy.AssignedAt.Truncate(time.Microsecond)
	legacy.QueuedAt = legacy.QueuedAt.Truncate(time.Microsecond)
	legacy.StartedAt = legacy.StartedAt.Truncate(time.Microsecond)
	require.NoError(t, server.apiStore.SaveRun(context.Background(), *legacy))
	command := controlplaneRunAssignedCommand(legacy)
	command.ID = "run-" + legacy.ID
	command.AgentID = "host-a"
	command.Status = controlplane.AgentCommandStatusQueued
	command.CreatedAt = time.Now().Add(-time.Minute).UTC()
	require.NoError(t, server.apiStore.SaveAgentCommand(context.Background(), command))

	stream := server.agents.Stream("host-a")
	published := stream.Subscribe()
	defer stream.Unsubscribe(published)
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	req.Header.Set("Idempotency-Key", operationKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var response struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, legacy.ID, response.RunID)

	select {
	case line := <-published:
		t.Fatalf("legacy destructive command was re-dispatched: %s", line)
	case <-time.After(50 * time.Millisecond):
	}
	adopted, found, err := server.apiStore.GetRunDispatch(context.Background(), legacy.ID, legacy.RequestFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, legacy.ID, adopted.ID)
	require.Equal(t, legacy.AssignedAt, adopted.AssignedAt)

	conflict := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy?allow_unsafe_state=true", nil)
	conflict.Header.Set("Idempotency-Key", operationKey)
	conflictRecorder := httptest.NewRecorder()
	server.ServeHTTP(conflictRecorder, conflict)
	require.Equal(t, http.StatusConflict, conflictRecorder.Code, conflictRecorder.Body.String())
}

func TestRunServiceStartApplyDispatchesAssignedRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       controlplane.AgentStatusOnline,
		Capabilities: []string{"network"},
	}))

	run, err := s.runs().StartApply(context.Background(), "lab", RunStartRequest{})
	require.NoError(t, err)
	require.Equal(t, "lab", run.Topology)
	require.Equal(t, "apply", run.Op)
	require.Equal(t, "host-a", run.AgentID)
	require.Equal(t, controlplane.RunAssigned, run.Status)
}

func TestRunServiceStartResetPersistsExactTargetAndDispatches(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	run, err := s.runs().StartReset(context.Background(), "lab", RunStartRequest{Target: "sysbox_node.web"})
	require.NoError(t, err)
	require.Equal(t, "reset", run.Op)
	require.Equal(t, "sysbox_node.web", run.Target)
	require.Equal(t, controlplane.RunAssigned, run.Status)
}

func TestRunServiceResumeAllowsFailedReset(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))
	parent := s.jobs.startWithOptions("lab", "reset", runStartOptions{Target: "sysbox_node.web", UnsafeState: true})
	s.jobs.finish(parent, errors.New("interrupted"))

	run, _, err := s.runs().Resume(context.Background(), parent.ID)
	require.NoError(t, err)
	require.Equal(t, "reset", run.Op)
	require.Equal(t, "sysbox_node.web", run.Target)
	require.True(t, run.UnsafeState)
}

func TestRunServiceResumeRejectsRunningParent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	parent := s.jobs.start("lab", "apply")
	s.jobs.markRunning(parent)

	run, gotParent, err := s.runs().Resume(context.Background(), parent.ID)
	require.Nil(t, run)
	require.Equal(t, parent.ID, gotParent.ID)
	require.ErrorContains(t, err, "still running")
	require.Equal(t, http.StatusConflict, runServiceStatus(err))
}

func writeRunServiceTopology(t *testing.T, s *Server, topology, hcl string) {
	t.Helper()
	path := s.workspaceService().HCLFile(topology)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(hcl), 0o644))
}
