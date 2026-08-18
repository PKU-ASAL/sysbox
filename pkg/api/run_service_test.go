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

	"github.com/stretchr/testify/require"

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
