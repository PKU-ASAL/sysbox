package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestKeyedLifecycleTargetSurvivesRestart(t *testing.T) {
	for _, op := range []string{"apply", "reset"} {
		t.Run(op, func(t *testing.T) {
			work, runs := t.TempDir(), t.TempDir()
			s := NewServer(work, runs)
			registerDockerAgent(t, s)
			revision := publishRevision(t, s, upsertApplyHCL)
			manager, err := s.stateManager("keyed")
			require.NoError(t, err)
			canonical, err := manager.LoadWithContext(context.Background())
			require.NoError(t, err)
			canonical.Resources = append(canonical.Resources, state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: canonical.TopologyUID, ResourceUID: "web-uid", Generation: 1})
			require.NoError(t, manager.SaveWithContext(context.Background(), canonical))
			submit := func(server *Server, target string) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(map[string]string{"revision": revision, "target": target})
				r := httptest.NewRequest("POST", "/v1/topologies/keyed/"+op, bytes.NewReader(raw))
				r.Header.Set("Idempotency-Key", "cli-operation")
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				return w
			}
			first := submit(s, "sysbox_node.web")
			require.Equal(t, 202, first.Code, first.Body.String())
			var response struct {
				RunID string `json:"run_id"`
			}
			require.NoError(t, json.Unmarshal(first.Body.Bytes(), &response))
			stored, err := s.apiStore.GetRun(context.Background(), response.RunID)
			require.NoError(t, err)
			require.Equal(t, "sysbox_node.web", stored.Target)
			restarted := NewServer(work, runs)
			replay := submit(restarted, "sysbox_node.web")
			require.Equal(t, 202, replay.Code, replay.Body.String())
			require.Contains(t, replay.Body.String(), response.RunID)
			require.Equal(t, 409, submit(restarted, "sysbox_node.other").Code)
			commands, err := restarted.apiStore.ListAgentCommands(context.Background(), "host-a")
			require.NoError(t, err)
			require.Len(t, commands, 1)
			claimed, err := restarted.jobs.claim(response.RunID, "host-a")
			require.NoError(t, err)
			require.NoError(t, restarted.jobs.finish(claimed, nil))
			require.Equal(t, controlplane.RunDone, claimed.Status)
			require.Equal(t, 202, submit(restarted, "sysbox_node.web").Code)
		})
	}
}

func TestKeyedApplyCanSelectUncreatedResource(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	revision := publishRevision(t, s, upsertApplyHCL)
	run, err := s.runs().StartApply(context.Background(), "new-target", RunStartRequest{OperationKey: "new-node", Revision: revision, Target: "sysbox_node.web"})
	require.NoError(t, err)
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, run.Target, commands[0].Run.Target)
	require.Empty(t, commands[0].ResourceUID, "creation is bound to topology and expected serial")
}

func TestKeyedResetResumeReplaysOriginalChildAfterRestart(t *testing.T) {
	work, runs := t.TempDir(), t.TempDir()
	s := NewServer(work, runs)
	registerDockerAgent(t, s)
	revision := publishRevision(t, s, upsertApplyHCL)
	snapshot, err := s.runs().snapshotForRevision(context.Background(), "lab", revision)
	require.NoError(t, err)
	parent := s.jobs.startWithOptions("lab", "reset", runStartOptions{Revision: revision, SnapshotPath: snapshot})
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	recorder := runtime.NewFileRecorder(checkpointPath, parent.ID, "lab")
	require.NoError(t, recorder.Begin("reset", &runtime.Plan{Actions: []model.PlannedChange{{Address: address.Resource("sysbox_node", "web"), Action: model.PlanActionReset}}}))
	cp, err := runtime.LoadCheckpointFile(checkpointPath)
	require.NoError(t, err)
	require.NoError(t, s.apiStore.SaveCheckpoint(context.Background(), "lab", parent.ID, *cp))
	require.NoError(t, s.jobs.finish(parent, errors.New("interrupted")))
	submit := func(server *Server) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/runs/"+parent.ID+"/resume", nil)
		r.Header.Set("Idempotency-Key", "resume-once")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	first := submit(s)
	require.Equal(t, 202, first.Code, first.Body.String())
	replay := submit(NewServer(work, runs))
	require.Equal(t, 202, replay.Code, replay.Body.String())
	require.JSONEq(t, first.Body.String(), replay.Body.String())
}

func TestRunCancelRequestsCommandStopWithoutCertifyingCompletion(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)
	revision := publishRevision(t, s, upsertApplyHCL)
	run, err := s.runs().StartApply(context.Background(), "lab", RunStartRequest{OperationKey: "cancel-test", Revision: revision})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/runs/"+run.ID+"/cancel", nil))
	require.Equal(t, 202, w.Code, w.Body.String())
	stored, err := s.apiStore.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, run.Status, stored.Status, "requesting cancellation is not completion evidence")
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	var cancellations int
	for _, command := range commands {
		if command.Type == "cancel_command" {
			cancellations++
			require.Equal(t, controlstore.RunCommandID(*run), command.Operation.ExternalID)
		}
	}
	require.Equal(t, 1, cancellations)
}

func TestRevisionPlanDoesNotAdmitRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	revision := publishRevision(t, s, `resource "sysbox_network" "lab" { cidr = "10.77.0.0/24" }`)
	raw, _ := json.Marshal(map[string]any{"revision": revision})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/topologies/lab/plan", bytes.NewReader(raw)))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "sysbox_network")
	runs, err := s.apiStore.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Empty(t, runs)
}

func TestRevisionPlanPreservesCurrentWorkspace(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", "# current")
	before, err := os.ReadFile(s.workspaceService().HCLFile("lab"))
	require.NoError(t, err)
	revision := publishRevision(t, s, `resource "sysbox_network" "lab" { cidr = "10.77.0.0/24" }`)
	raw, _ := json.Marshal(map[string]string{"revision": revision})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/topologies/lab/plan", bytes.NewReader(raw)))
	require.Equal(t, 200, w.Code, w.Body.String())
	after, err := os.ReadFile(s.workspaceService().HCLFile("lab"))
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
