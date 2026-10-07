package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func keyedApply(t *testing.T, s *Server, rev, key, value string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"revision": rev, "inputs": map[string]string{"flag": value}})
	req := httptest.NewRequest("POST", "/v1/topologies/keyed/apply", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestRunAdmissionAndOldSQLiteSnapshot(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		first := controlplane.Run{ID: "first", Topology: "admission", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
		require.NoError(t, s.SaveRun(ctx, first))
		second := first
		second.ID = "second"
		require.ErrorIs(t, s.SaveRun(ctx, second), errActiveOperation)
	})
	s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "test.db"), runsDir: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, s.SaveRun(ctx, controlplane.Run{ID: "old", Topology: "old", Status: controlplane.RunDone}))
	db, err := s.open()
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`UPDATE sysbox_runs SET record_json=x'' WHERE id='old'`)
	require.NoError(t, err)
	_, err = s.GetRun(ctx, "old")
	require.Error(t, err, "old rows must explicitly require rebuilding")
}

func TestKeyedApplyImmutableReplayAndHydration(t *testing.T) {
	work, runs := t.TempDir(), t.TempDir()
	s := NewServer(work, runs)
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	w := keyedApply(t, s, rev, "request-1", "private-canary")
	require.Equal(t, 202, w.Code, w.Body.String())
	var out struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	restart := NewServer(work, runs)
	query := httptest.NewRecorder()
	restart.ServeHTTP(query, httptest.NewRequest("GET", "/v1/runs/"+out.RunID, nil))
	require.Equal(t, 200, query.Code)
	require.Contains(t, query.Body.String(), `"inputs_required":true`)
	_, err := restart.jobs.claim(out.RunID, "host-a")
	require.Error(t, err, "claim after restart must await original inputs")
	require.Equal(t, 409, keyedApply(t, restart, rev, "request-1", "changed").Code)
	require.Equal(t, 202, keyedApply(t, restart, rev, "request-1", "private-canary").Code)
	claimed, err := restart.jobs.claim(out.RunID, "host-a")
	require.NoError(t, err)
	require.Equal(t, "private-canary", claimed.Inputs["flag"])
	require.NoError(t, restart.jobs.finish(claimed, nil))
	require.Equal(t, 202, keyedApply(t, restart, rev, "request-1", "private-canary").Code)
	stored, err := restart.apiStore.GetRun(context.Background(), out.RunID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunDone, stored.Status)
	terminalRestart := NewServer(work, runs)
	require.Equal(t, 202, keyedApply(t, terminalRestart, rev, "request-1", "private-canary").Code)
	terminal, ok := terminalRestart.jobs.get(out.RunID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunDone, terminal.Status)
	require.Empty(t, terminal.Inputs)
	raw, _ := json.Marshal(stored)
	require.NotContains(t, string(raw), "private-canary")
	commands, err := restart.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, runCommandID(*stored), commands[0].ID)
	raw, _ = json.Marshal(commands)
	require.NotContains(t, string(raw), "private-canary")
}

func TestKeyedApplyExcludesAnotherAdmission(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	require.Equal(t, 202, keyedApply(t, s, rev, "first", "secret").Code)
	w := keyedApply(t, s, rev, "second", "secret")
	require.Equal(t, 409, w.Code, w.Body.String())
}

func TestKeyedApplyConcurrentPostgresServers(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = isolatedPostgresTestDSN(t)
	servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
	registerDockerAgent(t, servers[0])
	rev := publishRevision(t, servers[0], upsertApplyHCL)
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses <- keyedApply(t, servers[i%2], rev, "shared", "private-canary")
		}(i)
	}
	wg.Wait()
	close(responses)
	var id string
	for w := range responses {
		require.Equal(t, 202, w.Code, w.Body.String())
		var out struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		if id == "" {
			id = out.RunID
		}
		require.Equal(t, id, out.RunID)
	}
	runs, err := servers[0].apiStore.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, latestRunsByID(runs), 1)
	commands, err := servers[0].apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}

func TestRunAdmissionSerializesConcurrentDifferentKeys(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, id := range []string{"first", "second"} {
			go func(id string) {
				<-start
				run := controlplane.Run{ID: id, Topology: "lab", Op: "apply", AgentID: "agent", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
				_, _, err := s.CreateRunDispatch(ctx, incarnationDispatch(run))
				results <- err
			}(id)
		}
		close(start)
		success := 0
		for range 2 {
			err := <-results
			if err == nil {
				success++
			} else {
				require.ErrorIs(t, err, errActiveOperation)
			}
		}
		require.Equal(t, 1, success)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
	})
}

type unavailableRunStore struct{ apiStore }

func (s unavailableRunStore) GetRun(context.Context, string) (*controlplane.Run, error) {
	return nil, errors.New("backend unavailable")
}

func TestRunQueriesAndClaimReportUnavailable(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.apiStore = unavailableRunStore{s.apiStore}
	s.jobs.store = s.apiStore
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/example", nil))
	require.Equal(t, 503, w.Code)
	w = httptest.NewRecorder()
	s.handleClaimAgentRunByID(w, "agent", "example", controlplane.RunClaim{QueuedAt: time.Now()})
	require.Equal(t, 503, w.Code)
}

func TestKeyedApplySnapshotFeedsFollowingDestroy(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	w := keyedApply(t, s, rev, "first", "secret")
	require.Equal(t, 202, w.Code, w.Body.String())
	var accepted struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &accepted))
	run, ok := s.jobs.get(accepted.RunID)
	require.True(t, ok)
	require.NoError(t, s.jobs.finish(run, nil))
	destroy, err := s.runs().StartDestroyWithOperationKey(context.Background(), "keyed", false, "destroy")
	require.NoError(t, err)
	require.Equal(t, run.SnapshotPath, destroy.SnapshotPath)
}

func TestKeyedApplyEmptyInputsCanonicalAndFingerprintImmutable(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	first, err := s.runs().StartApply(context.Background(), "canonical", RunStartRequest{Revision: rev, OperationKey: "key"})
	require.NoError(t, err)
	second, err := s.runs().StartApply(context.Background(), "canonical", RunStartRequest{Revision: rev, OperationKey: "key", Inputs: map[string]string{}})
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	changed := *first
	changed.RequestFingerprint = "changed"
	require.Error(t, s.apiStore.SaveRun(context.Background(), changed))
	require.False(t, persistence.SameRunIncarnation(*first, changed))
}

func TestRejectedUnkeyedApplyDoesNotChangeWorkspace(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	first := publishRevision(t, s, upsertApplyHCL)
	other := publishRevision(t, s, upsertApplyHCL+"\n# other\n")
	_, err := s.runs().StartApply(context.Background(), "guard", RunStartRequest{Revision: first})
	require.NoError(t, err)
	_, err = s.runs().StartApply(context.Background(), "guard", RunStartRequest{Revision: other})
	require.Error(t, err)
	raw, err := os.ReadFile(s.workspaceService().HCLFile("guard"))
	require.NoError(t, err)
	require.Equal(t, upsertApplyHCL, string(raw))
}

func TestWorkspaceLifecyclePinsSnapshot(t *testing.T) {
	for _, operation := range []string{"apply", "repair", "reset"} {
		t.Run(operation, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			registerDockerAgent(t, s)
			writeRunServiceTopology(t, s, "workspace", upsertApplyHCL)
			start := s.runs().StartApply
			if operation == "repair" {
				start = s.runs().StartRepair
			}
			if operation == "reset" {
				start = s.runs().StartReset
			}
			run, err := start(context.Background(), "workspace", RunStartRequest{})
			require.NoError(t, err)
			require.NotEmpty(t, run.SnapshotPath)
		})
	}
}

func TestHydrationCannotAuthorizePreviouslyCapturedMissingInputs(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	req := RunStartRequest{Revision: rev, OperationKey: "captured", Inputs: map[string]string{"secret": "value"}}
	run, err := s.runs().StartApply(context.Background(), "captured", req)
	require.NoError(t, err)
	intent, err := s.apiStore.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	s.jobs.mu.Lock()
	s.jobs.runs[run.ID].Inputs = nil
	s.jobs.mu.Unlock()
	s.jobs.mu.RLock()
	captured := inputsForRun(s.jobs.runs[run.ID], intent)
	s.jobs.mu.RUnlock()
	_, err = s.runs().StartApply(context.Background(), "captured", req)
	require.NoError(t, err)
	require.False(t, s.jobs.inputsRequired(intent), "current cache was hydrated")
	require.True(t, keyedInputsRequired(intent, captured), "a claim must still reject its previously captured nil inputs")
}

func TestRunRequestFingerprintCannotBeRewritten(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "bound", Topology: "bound", Op: "apply", Status: controlplane.RunAssigned, RequestFingerprint: "apply-request:original"}
		require.NoError(t, s.SaveRun(ctx, run))
		changed := run
		changed.RequestFingerprint = ""
		require.ErrorIs(t, s.SaveRun(ctx, changed), persistence.ErrConflict)
		actual, err := s.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, run.RequestFingerprint, actual.RequestFingerprint)
	})
}
