package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestJobsOperationKeyIsAtomicAndPersistsAcrossRestart(t *testing.T) {
	runsDir := t.TempDir()
	jobs := newJobsWithRecovery(runsDir, nil, false)
	const workers = 32
	results := make(chan *controlplane.Run, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- jobs.startWithOptions("mixed", "destroy", runStartOptions{OperationKey: "destroy:cf-mixed-g1"})
		}()
	}
	group.Wait()
	close(results)
	var runID string
	for run := range results {
		if runID == "" {
			runID = run.ID
		}
		require.Equal(t, runID, run.ID)
		require.Equal(t, "destroy:cf-mixed-g1", run.OperationKey)
	}
	require.Len(t, jobs.list("mixed"), 1)

	reloaded := newJobsWithRecovery(runsDir, nil, false)
	replayed := reloaded.startWithOptions("mixed", "destroy", runStartOptions{OperationKey: "destroy:cf-mixed-g1"})
	require.Equal(t, runID, replayed.ID)
	require.Len(t, reloaded.list("mixed"), 1)
}

func TestJobsOperationKeyIsScopedByTopologyAndOperation(t *testing.T) {
	jobs := newJobsWithRecovery(t.TempDir(), nil, false)
	first := jobs.startWithOptions("one", "destroy", runStartOptions{OperationKey: "same-key"})
	otherTopology := jobs.startWithOptions("two", "destroy", runStartOptions{OperationKey: "same-key"})
	otherOperation := jobs.startWithOptions("one", "apply", runStartOptions{OperationKey: "same-key"})
	require.NotEqual(t, first.ID, otherTopology.ID)
	require.NotEqual(t, first.ID, otherOperation.ID)
}

func TestValidateOperationKeyBounds(t *testing.T) {
	require.NoError(t, validateOperationKey("destroy:cf-mixed-g1"))
	for _, invalid := range []string{"", "contains space", "contains/slash", string(make([]byte, 129))} {
		require.Error(t, validateOperationKey(invalid), invalid)
	}
}

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
	require.Equal(t, controlplane.RunFailed, run.Status)
	require.True(t, run.Recoverable)
	require.Equal(t, "server restarted before run completion", run.Err)
}

func TestJobsLoadCheckpointsDoesNotOverridePersistedRun(t *testing.T) {
	runsDir := t.TempDir()
	runID := "run-done"
	require.NoError(t, os.MkdirAll(filepath.Join(runsDir, "mixed", "runs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsDir, "mixed"), 0o755))

	persisted := controlplane.Run{
		ID:        runID,
		Topology:  "mixed",
		Op:        "apply",
		Status:    controlplane.RunDone,
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
	require.Equal(t, controlplane.RunDone, run.Status)
	require.False(t, run.Recoverable)
}

func TestJobsPersistsQueuedRunAndReloadsAsQueued(t *testing.T) {
	runsDir := t.TempDir()
	jobs := newJobs(runsDir, nil)
	run := jobs.start("mixed", "apply")

	reloaded := newJobs(runsDir, nil)
	got, ok := reloaded.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunQueued, got.Status)
	require.False(t, got.Recoverable)
	require.Empty(t, got.Err)
}

func TestJobsReloadsAssignedRunAsRecoverable(t *testing.T) {
	runsDir := t.TempDir()
	jobs := newJobs(runsDir, nil)
	run := jobs.start("mixed", "apply")
	jobs.assign(run, DefaultAgentID)

	reloaded := newJobs(runsDir, nil)
	got, ok := reloaded.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
	require.True(t, got.Recoverable)
	require.Equal(t, "server restarted before run completion", got.Err)
}
