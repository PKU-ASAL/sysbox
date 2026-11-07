package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func resetWithInputs(t *testing.T, s *Server, rev, key, value string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]any{"revision": rev, "inputs": map[string]string{"password": value}})
	req := httptest.NewRequest("POST", "/v1/topologies/lab/reset", bytes.NewReader(raw))
	req.Header.Set("Idempotency-Key", key)
	out := httptest.NewRecorder()
	s.ServeHTTP(out, req)
	return out
}
func TestResetInputsSurviveOnlyByExactReplay(t *testing.T) {
	work, runs := t.TempDir(), t.TempDir()
	s := NewServer(work, runs)
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	out := resetWithInputs(t, s, rev, "reset-once", "private-canary")
	require.Equal(t, 202, out.Code, out.Body.String())
	var result struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &result))
	cold := NewServer(work, runs)
	stored, err := cold.apiStore.GetRun(context.Background(), result.RunID)
	require.NoError(t, err)
	require.Empty(t, stored.Inputs)
	require.True(t, cold.jobs.inputsRequired(stored))
	_, err = cold.jobs.claim(result.RunID, "host-a")
	require.ErrorIs(t, err, errApplyInputsRequired)
	require.Equal(t, 409, resetWithInputs(t, cold, rev, "reset-once", "changed").Code)
	require.Equal(t, 202, resetWithInputs(t, cold, rev, "reset-once", "private-canary").Code)
	claimed, err := cold.jobs.claim(result.RunID, "host-a")
	require.NoError(t, err)
	require.Equal(t, "private-canary", claimed.Inputs["password"])
	require.NoError(t, controlplane.ValidateRunInputs(*claimed, claimed.Inputs))
	require.Error(t, controlplane.ValidateRunInputs(*claimed, map[string]string{"password": "changed"}))
	commands, err := cold.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	raw, _ := json.Marshal(commands)
	require.NotContains(t, string(raw), "private-canary")
}

func TestResetContinuationInputsRemainBoundAfterRestart(t *testing.T) {
	work, runs := t.TempDir(), t.TempDir()
	s := NewServer(work, runs)
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	response := resetWithInputs(t, s, rev, "parent", "private-canary")
	require.Equal(t, 202, response.Code, response.Body.String())
	var accepted struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
	parent, ok := s.jobs.get(accepted.RunID)
	require.True(t, ok)
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	recorder := runtime.NewFileRecorder(checkpointPath, parent.ID, "lab")
	require.NoError(t, recorder.Begin("reset", &runtime.Plan{Actions: []model.PlannedChange{{Address: address.Resource("sysbox_node", "web"), Action: model.PlanActionReset}}}))
	cp, err := runtime.LoadCheckpointFile(checkpointPath)
	require.NoError(t, err)
	require.NoError(t, s.apiStore.SaveCheckpoint(context.Background(), "lab", parent.ID, *cp))
	require.NoError(t, s.jobs.finish(parent, errors.New("interrupted")))
	submit := func(server *Server, value string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"inputs": map[string]string{"password": value}})
		req := httptest.NewRequest("POST", "/v1/runs/"+parent.ID+"/resume", bytes.NewReader(raw))
		req.Header.Set("Idempotency-Key", "continue")
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		return out
	}
	require.Equal(t, 409, submit(s, "changed").Code)
	first := submit(s, "private-canary")
	require.Equal(t, 202, first.Code, first.Body.String())
	var childResult struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &childResult))
	cold := NewServer(work, runs)
	_, err = cold.jobs.claim(childResult.RunID, "host-a")
	require.ErrorIs(t, err, errApplyInputsRequired)
	replay := submit(cold, "private-canary")
	require.Equal(t, 202, replay.Code, replay.Body.String())
	require.JSONEq(t, first.Body.String(), replay.Body.String())
	child, err := cold.jobs.claim(childResult.RunID, "host-a")
	require.NoError(t, err)
	require.NoError(t, controlplane.ValidateRunInputs(*child, child.Inputs))
	require.Equal(t, "private-canary", child.Inputs["password"])
	stored, err := cold.apiStore.GetRun(context.Background(), child.ID)
	require.NoError(t, err)
	raw, _ := json.Marshal(stored)
	require.NotContains(t, string(raw), "private-canary")
}

func TestResetInputsRejectUnboundAndTrailingRequests(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	require.Equal(t, 400, resetWithInputs(t, s, rev, "", "private").Code)
	for _, suffix := range []string{` {}`, ` garbage`} {
		r := httptest.NewRequest("POST", "/v1/runs/parent/resume", bytes.NewBufferString(`{"inputs":{}}`+suffix))
		r.Header.Set("Idempotency-Key", "resume")
		out := httptest.NewRecorder()
		s.ServeHTTP(out, r)
		require.Equal(t, 400, out.Code, out.Body.String())
	}
}

func TestResetRequestRejectsOversizeAndTrailingData(t *testing.T) {
	for _, body := range []string{`{"inputs":{}} {}`, `{"inputs":{}} garbage`, `{"unexpected":true}`, `{"inputs":{"password":"` + strings.Repeat("a", 1<<20) + `"}}`} {
		_, err := decodeResetRequest(httptest.NewRequest("POST", "/reset", strings.NewReader(body)))
		require.Error(t, err)
	}
}
