package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

// Uses the production signed HTTP boundary, including the original durable
// command/grant. No test-only worker completion writer is needed.
type hostRunFixture struct {
	server *Server
	run    controlplane.Run
	grant  controlplane.ExecutionGrant
	key    ed25519.PrivateKey
}

func prepareHostRunFixture(t *testing.T, server *Server, run controlplane.Run, topologyUID string) *hostRunFixture {
	t.Helper()
	server.agentSvc = newAgentService(server)
	ctx := context.Background()
	current, err := server.apiStore.GetRun(ctx, run.ID)
	require.NoError(t, err)
	run = *current
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	if server.grantHostKeys == nil {
		server.grantHostKeys = map[string]ed25519.PublicKey{}
	}
	server.grantHostKeys[run.AgentID] = public
	command := controlplane.AgentCommand{ID: "host-command-" + run.ID, AgentID: run.AgentID, Type: "run_assigned", Status: controlplane.AgentCommandStatusDelivered, Run: &run, TopologyUID: topologyUID, ActionID: run.ID, Attempt: 1, LeaseOwner: "delivery-worker", LeaseUntil: time.Now().Add(time.Minute)}
	require.NoError(t, server.apiStore.SaveAgentCommand(ctx, command))
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: server.apiStore})
	session, err := repo.OpenSession(ctx, run.AgentID, "host-result", "")
	require.NoError(t, err)
	req := hostsupervisor.RunRequest{RunID: run.ID, Topology: run.Topology, TopologyUID: topologyUID, Revision: run.Revision, ActionID: command.ActionID, Action: run.Operation, Target: run.Target}
	digest, err := req.Digest()
	require.NoError(t, err)
	grant, err := repo.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: topologyUID}, AgentID: run.AgentID, SessionID: session.ID, CommandID: command.ID, ActionID: command.ActionID, Attempt: 1, Operation: "run_" + run.Operation, Digest: digest, TargetBootID: "test-host"}, time.Minute)
	require.NoError(t, err)
	return &hostRunFixture{server: server, run: run, grant: *grant, key: key}
}

func (f *hostRunFixture) proof(t *testing.T, result controlplane.Run) hostRunCompletion {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	receipt, err := controlplane.SignRunStopReceipt(f.key, f.grant, "test-host", "result-"+f.run.ID, raw, result.EndedAt)
	require.NoError(t, err)
	return hostRunCompletion{Receipt: receipt, Evidence: raw}
}
func (f *hostRunFixture) post(t *testing.T, result controlplane.Run) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(f.proof(t, result))
	require.NoError(t, err)
	response := httptest.NewRecorder()
	f.server.mux.ServeHTTP(response, httptest.NewRequest("POST", "/v1/hosts/"+f.run.AgentID+"/runs/"+f.run.ID+"/completed", bytes.NewReader(raw)))
	return response
}
func (f *hostRunFixture) complete(t *testing.T, result controlplane.Run) *controlplane.Run {
	t.Helper()
	response := f.post(t, result)
	require.Equal(t, 200, response.Code, response.Body.String())
	stored, err := f.server.apiStore.GetRun(context.Background(), f.run.ID)
	require.NoError(t, err)
	return stored
}
