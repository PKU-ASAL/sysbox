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
	"github.com/oslab/sysbox/pkg/runtime"
)

func TestListRuns(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runs, "mixed"), 0o755))
	writeRunRecord(t, runs, controlplane.Run{
		ID:        "run-1",
		Topology:  "mixed",
		Op:        "apply",
		Status:    controlplane.RunDone,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	})
	writeRunRecord(t, runs, controlplane.Run{
		ID:        "run-2",
		Topology:  "other",
		Op:        "destroy",
		Status:    controlplane.RunFailed,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/runs?topology=mixed", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Runs []controlplane.Run `json:"runs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Runs, 1)
	require.Equal(t, "run-1", body.Runs[0].ID)
}

func TestGetRunActions(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	run := controlplane.Run{
		ID:        "run-actions",
		Topology:  "mixed",
		Op:        "apply",
		Status:    controlplane.RunDone,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	}
	writeRunRecord(t, runs, run)
	cp := runtime.OperationCheckpoint{
		RunID:     run.ID,
		Topology:  run.Topology,
		Operation: run.Op,
		Status:    runtime.OperationDone,
		Steps: []runtime.OperationStep{{
			Index:    0,
			Resource: "sysbox_node.vm",
			Action:   controlplane.PlanActionCreate,
			Kind:     "resource",
			Status:   runtime.OperationDone,
		}},
	}
	raw, err := json.Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(runs, run.Topology, "runs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runs, run.Topology, "runs", run.ID+".checkpoint.json"), raw, 0o644))

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/runs/run-actions/actions", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body RunActionLog
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, run.ID, body.RunID)
	require.Len(t, body.Actions, 1)
	require.Equal(t, "sysbox_node.vm", body.Actions[0].Resource)
}

func TestRunEventsLoadFromPersistedRunAfterServerRestart(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	run := controlplane.Run{
		ID:        "run-events",
		ProjectID: "default",
		Workspace: "mixed",
		Topology:  "mixed",
		Op:        "apply",
		Status:    controlplane.RunDone,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	}
	writeRunRecord(t, runs, run)
	cp := runtime.OperationCheckpoint{
		RunID:     run.ID,
		Topology:  run.Topology,
		Operation: run.Op,
		Status:    runtime.OperationDone,
		Steps: []runtime.OperationStep{{
			Index:     0,
			Resource:  "sysbox_node.vm",
			Action:    controlplane.PlanActionCreate,
			Kind:      "resource",
			Status:    runtime.OperationDone,
			StartedAt: time.Now().UTC(),
		}},
	}
	raw, err := json.Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(runs, run.Topology, "runs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runs, run.Topology, "runs", run.ID+".checkpoint.json"), raw, 0o644))

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/runs/run-events/events", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"workspace":"mixed"`)
	require.Contains(t, rec.Body.String(), `"resource":"sysbox_node.vm"`)
}

func writeRunRecord(t *testing.T, runsDir string, run controlplane.Run) {
	t.Helper()
	dir := filepath.Join(runsDir, run.Topology)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	path := filepath.Join(dir, "runs.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	require.NoError(t, err)
}
