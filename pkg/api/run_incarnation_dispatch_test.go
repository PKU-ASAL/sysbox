package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestRunDispatchSeparatesRestartedIncarnations(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		run := controlplane.Run{ID: "reused", Topology: "lab", Operation: "apply", Op: "apply", OperationKey: "key", Revision: "rev", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC()}
		require.NoError(t, s.SaveRun(ctx, run))
		assigned := run
		assigned.MarkAssigned("agent", time.Now().UTC())
		old := incarnationDispatch(assigned)
		_, _, err := s.CreateRunDispatch(ctx, old)
		require.NoError(t, err)
		done, err := s.GetRun(ctx, run.ID)
		require.NoError(t, err)
		done.Status = controlplane.RunDone
		done.EndedAt = time.Now().UTC()
		require.NoError(t, s.SaveRun(ctx, *done))
		next := run
		next.QueuedAt = run.QueuedAt.Add(time.Second)
		_, err = s.RestartRun(ctx, next)
		require.NoError(t, err)
		next.MarkAssigned("agent", time.Now().UTC())
		fresh := incarnationDispatch(next)
		require.NotEqual(t, old.Command.ID, fresh.Command.ID)
		_, _, err = s.CreateRunDispatch(ctx, fresh)
		require.NoError(t, err)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 2)
		_, _, err = s.CreateRunDispatch(ctx, old)
		require.Error(t, err, "old request must not resolve to a new incarnation")
		stored, err := s.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.True(t, next.QueuedAt.Equal(stored.QueuedAt))
		require.Equal(t, controlplane.RunAssigned, stored.Status)
		service := &AgentService{store: s}
		reconciled, terminal := service.ReconcileCommandState(ctx, old.Command)
		require.True(t, terminal)
		require.Equal(t, controlplane.AgentCommandStatusCancelled, reconciled.Status)
		require.NoError(t, service.RepublishAssignedRuns(ctx), "historical assigned snapshots must not be repaired as current")
	})
}

func incarnationDispatch(run controlplane.Run) controlstore.RunDispatchRequest {
	return controlstore.RunDispatchRequest{Run: run, Fingerprint: runAssignmentFingerprint(run), Command: controlplane.AgentCommand{TopologyUID: "owner", ID: controlstore.RunCommandID(run), AgentID: run.AgentID, Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, Run: &run}}
}

func TestClaimEndpointRejectsMissingAndOldIncarnation(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline}))
	run := s.jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent"})
	require.NoError(t, s.jobs.assign(run, "agent"))
	for _, old := range []bool{false, true} {
		var raw []byte
		if old {
			var err error
			raw, err = json.Marshal(controlplane.RunClaim{QueuedAt: run.QueuedAt.Add(-time.Second), Revision: run.Revision})
			require.NoError(t, err)
		}
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/agents/agent/runs/"+run.ID+"/claim", bytes.NewReader(raw)))
		if old {
			require.Equal(t, http.StatusConflict, r.Code, r.Body.String())
		} else {
			require.Equal(t, http.StatusBadRequest, r.Code, r.Body.String())
		}
	}
	stored, err := s.apiStore.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunAssigned, stored.Status)
}
