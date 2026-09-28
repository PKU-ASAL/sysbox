package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestLocalObjectReaderRejectsIncompleteInventory(t *testing.T) {
	for _, kind := range []string{"malformed-json", "read-error", "bad-pattern"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good"}`), 0600))
			path := filepath.Join(dir, "bad.json")
			pattern := filepath.Join(dir, "*.json")
			switch kind {
			case "malformed-json":
				require.NoError(t, os.WriteFile(path, []byte(`{"secret":"canary-secret",`), 0600))
			case "read-error":
				require.NoError(t, os.Mkdir(path, 0700))
			case "bad-pattern":
				pattern = filepath.Join(dir, "[")
			}
			items, err := readLocalObjects[controlplane.Agent](pattern)
			require.Error(t, err)
			require.Empty(t, items, "partial inventory must not authorize mutation")
			require.NotContains(t, err.Error(), "canary-secret")
		})
	}
}

func TestLocalObjectReaderMissingDirectoryIsEmpty(t *testing.T) {
	items, err := readLocalObjects[controlplane.Agent](filepath.Join(t.TempDir(), "missing", "*.json"))
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestLocalObjectReaderReportsDirectoryScanError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	_, err := readLocalObjects[controlplane.Agent](filepath.Join(path, "*.json"))
	require.Error(t, err, "directory scan failures must not become an empty inventory")
}

func TestLocalObjectAtomicReplacementWithConcurrentReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	type record struct{ Payload string }
	require.NoError(t, writeLocalObject(path, record{Payload: strings.Repeat("a", 256*1024)}))
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for range 30 {
				if worker < 4 {
					if err := writeLocalObject(path, record{Payload: strings.Repeat("b", 256*1024)}); err != nil {
						t.Error(err)
						return
					}
				} else {
					got, err := readLocalObject[record](path)
					if err != nil {
						t.Error(err)
						return
					}
					if len(got.Payload) != 256*1024 {
						t.Error("incomplete record")
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func TestLocalRunHistoryRejectsCorruption(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	require.NoError(t, store.SaveRun(context.Background(), controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunRunning}))
	path := filepath.Join(store.runsDir, "lab", "runs.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = file.WriteString("{broken\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	runs, err := store.LoadRuns(context.Background())
	require.Error(t, err)
	require.Empty(t, runs)
	_, err = store.GetRun(context.Background(), "run")
	require.Error(t, err)
}

func TestLocalRunHistoryReadsLargeRecordsWithoutTruncating(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	run := controlplane.Run{ID: "run", Topology: "lab", Err: strings.Repeat("x", 128*1024)}
	require.NoError(t, store.SaveRun(context.Background(), run))
	got, err := store.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, run.Err, got.Err)
}

func TestLocalCommandHistoryRejectsCorruption(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	path := filepath.Join(store.runsDir, "_agents", "host", "command-events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(`{"command_id":"good"}`+"\n{broken\n"), 0600))
	records, err := store.ListAgentCommandEvents(context.Background(), "host")
	require.Error(t, err)
	require.Empty(t, records)
}

func TestLocalCommandSaveDoesNotOverwriteCorruptRecord(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	path := filepath.Join(store.runsDir, "_agents", "host", "commands", "cmd.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	original := []byte(`{"status":`)
	require.NoError(t, os.WriteFile(path, original, 0600))
	err := store.SaveAgentCommand(context.Background(), controlplane.AgentCommand{ID: "cmd", AgentID: "host", Status: controlplane.AgentCommandStatusQueued})
	require.Error(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestLocalGuestCASDoesNotHideCorruption(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	execution := controlplane.GuestExecution{ID: "exec", Version: 1, Status: controlplane.GuestExecutionQueued}
	require.NoError(t, store.SaveGuestExecution(context.Background(), execution))
	path := filepath.Join(store.runsDir, "_guest-executions", "exec.json")
	require.NoError(t, os.WriteFile(path, []byte(`{broken`), 0600))
	ok, err := store.CompareAndSwapGuestExecution(context.Background(), execution, 1)
	require.False(t, ok)
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax, "corruption must remain distinguishable from not found")
}

func TestLocalRevisionReadErrorIsNotNotFound(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	path := filepath.Join(store.runsDir, "global-revisions", "revision.json")
	require.NoError(t, os.MkdirAll(path, 0700))
	_, err := store.GetGlobalRevision(context.Background(), "revision")
	require.Error(t, err)
	require.NotErrorIs(t, err, errGlobalRevisionNotFound)
}

func TestLocalRunDispatchDoesNotRecreateAfterCorruptHistory(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	path := filepath.Join(store.runsDir, "lab", "runs.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(`{broken`), 0600))
	_, _, err := store.CreateRunDispatch(context.Background(), RunDispatchRequest{
		Run:         controlplane.Run{ID: "run", Topology: "lab"},
		Command:     controlplane.AgentCommand{ID: "cmd", AgentID: "host"},
		Fingerprint: "request",
	})
	require.Error(t, err, "a read failure does not authorize a fresh dispatch")
	require.NoFileExists(t, filepath.Join(store.runsDir, "_run_requests", "run.json"))
}

func TestLocalNullRecordsAreCorrupt(t *testing.T) {
	for _, raw := range []string{"null", "[]", "42"} {
		t.Run(raw, func(t *testing.T) {
			store := &localAPIStore{runsDir: t.TempDir()}
			path := filepath.Join(store.runsDir, "_agents", "host", "commands", "cmd.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
			err := store.SaveAgentCommand(context.Background(), controlplane.AgentCommand{ID: "cmd", AgentID: "host"})
			require.ErrorIs(t, err, errCorruptLocalRecord)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, raw, string(got))
			history := filepath.Join(store.runsDir, "lab", "runs.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(history), 0700))
			require.NoError(t, os.WriteFile(history, []byte(raw+"\n"), 0600))
			_, err = store.LoadRuns(context.Background())
			require.ErrorIs(t, err, errCorruptLocalRecord)
		})
	}
}

func TestLocalRunReaderWaitsForCommittedAppend(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	require.NoError(t, store.SaveRun(context.Background(), controlplane.Run{ID: "first", Topology: "lab"}))
	path := filepath.Join(store.runsDir, "lab", "runs.jsonl")
	writerLock := flock.New(path + ".lock")
	require.NoError(t, writerLock.Lock())
	t.Cleanup(func() { _ = writerLock.Unlock() })
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	_, err = f.WriteString(`{"id":"second"`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = store.LoadRuns(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "an in-flight append is not committed corruption")
	_, err = f.WriteString(`,"topology":"lab"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, writerLock.Unlock())
	runs, err := store.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, runs, 2)
}

func TestLocalRunHistoryRejectsUncommittedTail(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	path := filepath.Join(store.runsDir, "lab", "runs.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"tail"}`), 0600))
	_, err := store.LoadRuns(context.Background())
	require.ErrorIs(t, err, errCorruptLocalRecord)
}
