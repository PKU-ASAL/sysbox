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

	"github.com/oslab/sysbox/pkg/runtime"
)

func TestListRuns(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runs, "mixed"), 0o755))
	writeRunRecord(t, runs, Run{
		ID:        "run-1",
		Topology:  "mixed",
		Op:        "apply",
		Status:    RunDone,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	})
	writeRunRecord(t, runs, Run{
		ID:        "run-2",
		Topology:  "other",
		Op:        "destroy",
		Status:    RunFailed,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/runs?topology=mixed", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Runs []Run `json:"runs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Runs, 1)
	require.Equal(t, "run-1", body.Runs[0].ID)
}

func TestGetRunActions(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	run := Run{
		ID:        "run-actions",
		Topology:  "mixed",
		Op:        "apply",
		Status:    RunDone,
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
			Action:   runtime.PlanActionCreate,
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

func writeRunRecord(t *testing.T, runsDir string, run Run) {
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
