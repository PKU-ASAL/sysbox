package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJobsLoadCheckpointsMarksInterruptedRunRecoverable(t *testing.T) {
	runsDir := t.TempDir()
	runID := "run-interrupted"
	cpDir := filepath.Join(runsDir, "mixed", "runs")
	require.NoError(t, os.MkdirAll(cpDir, 0o755))

	started := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	data, err := json.Marshal(map[string]any{
		"run_id":     runID,
		"topology":   "mixed",
		"operation":  "apply",
		"status":     "started",
		"started_at": started,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cpDir, runID+".checkpoint.json"), data, 0o644))

	jobs := newJobs(runsDir, nil)
	run, ok := jobs.get(runID)
	require.True(t, ok)
	require.Equal(t, "mixed", run.Topology)
	require.Equal(t, "apply", run.Op)
	require.Equal(t, RunFailed, run.Status)
	require.True(t, run.Recoverable)
	require.Equal(t, "server restarted before run completion", run.Err)
}

func TestJobsLoadCheckpointsDoesNotOverridePersistedRun(t *testing.T) {
	runsDir := t.TempDir()
	runID := "run-done"
	require.NoError(t, os.MkdirAll(filepath.Join(runsDir, "mixed", "runs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsDir, "mixed"), 0o755))

	persisted := Run{
		ID:        runID,
		Topology:  "mixed",
		Op:        "apply",
		Status:    RunDone,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	}
	line, err := json.Marshal(persisted)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, "mixed", "runs.jsonl"), append(line, '\n'), 0o644))

	checkpoint := []byte(`{"run_id":"run-done","topology":"mixed","operation":"apply","status":"started"}`)
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, "mixed", "runs", runID+".checkpoint.json"), checkpoint, 0o644))

	jobs := newJobs(runsDir, nil)
	run, ok := jobs.get(runID)
	require.True(t, ok)
	require.Equal(t, RunDone, run.Status)
	require.False(t, run.Recoverable)
}

func TestJobsPersistsQueuedRunAndReloadsAsQueued(t *testing.T) {
	runsDir := t.TempDir()
	jobs := newJobs(runsDir, nil)
	run := jobs.start("mixed", "apply")

	reloaded := newJobs(runsDir, nil)
	got, ok := reloaded.get(run.ID)
	require.True(t, ok)
	require.Equal(t, RunQueued, got.Status)
	require.False(t, got.Recoverable)
	require.Empty(t, got.Err)
}

func TestJobsReloadsAssignedRunAsRecoverable(t *testing.T) {
	runsDir := t.TempDir()
	jobs := newJobs(runsDir, nil)
	run := jobs.start("mixed", "apply")
	jobs.assign(run, DefaultWorkerID)

	reloaded := newJobs(runsDir, nil)
	got, ok := reloaded.get(run.ID)
	require.True(t, ok)
	require.Equal(t, RunFailed, got.Status)
	require.True(t, got.Recoverable)
	require.Equal(t, "server restarted before run completion", got.Err)
}
