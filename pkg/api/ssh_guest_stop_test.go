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
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHStopCannotBypassCanonicalCompletion(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "session", "")
		require.NoError(t, err)
		req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 2, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
		digest, err := req.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: req.ID, Attempt: 1, Operation: "guest_ssh_access_delete", Digest: digest, TargetBootID: "guest"}, time.Minute)
		require.NoError(t, err)
		result := substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{ID: req.ID, Digest: digest, NodeID: "web", User: "ubuntu", AccessID: "admin", Revision: 3, BootID: "guest", TopologyUID: "owner", ResourceUID: "node", Generation: 1, Epoch: grant.Epoch}}
		receipt, err := controlplane.SignGuestSSHAccessStopReceipt(key, *grant, "host", req, result, "receipt", time.Now())
		require.NoError(t, err)
		raw, err := json.Marshal(receipt)
		require.NoError(t, err)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
		require.Equal(t, 409, response.Code, response.Body.String())
		require.NoError(t, authority.Authorize(ctx, *grant), "scope must remain held until canonical SSH result is committed")
		op := prepareNodeOperation(controlplane.NodeOperation{ID: req.ID, ActionID: req.ID, CommandID: "command", AgentID: "agent", Topology: "lab", Node: "web", ResourceUID: "node", Generation: 1, Operation: "guest_ssh_access_delete", SSHMutation: &req})
		require.NoError(t, store.CreateNodeOperation(ctx, op))
		command := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ActionID: req.ID, ResourceUID: "node", Generation: 1, Operation: op, Attempt: 1}
		require.NoError(t, store.SaveAgentCommand(ctx, command))
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", command.ID))
		foreign := result
		foreign.Handle.NodeID = "other-node"
		wrong, err := controlplane.SignGuestSSHAccessStopReceipt(key, *grant, "host", req, foreign, "wrong", time.Now())
		require.NoError(t, err)
		wrongBody, err := json.Marshal(HostNodeOperationCompletion{Receipt: wrong})
		require.NoError(t, err)
		rejected := httptest.NewRecorder()
		s.ServeHTTP(rejected, httptest.NewRequest("POST", "/v1/hosts/agent/node-operations/delete/completed", bytes.NewReader(wrongBody)))
		require.Equal(t, 409, rejected.Code, "signed result for another node cannot complete this operation")
		require.NoError(t, authority.Authorize(ctx, *grant))
		body, err := json.Marshal(HostNodeOperationCompletion{Receipt: receipt})
		require.NoError(t, err)
		for range 2 {
			completed := httptest.NewRecorder()
			s.ServeHTTP(completed, httptest.NewRequest("POST", "/v1/hosts/agent/node-operations/delete/completed", bytes.NewReader(body)))
			require.Equal(t, 200, completed.Code, completed.Body.String())
		}
		stored, err := store.GetNodeOperation(ctx, req.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.NodeOperationStatusDone, stored.Status)
		require.Equal(t, &result, stored.SSHResult)
		require.Error(t, authority.Authorize(ctx, *grant))
	})
}

func TestGuestSSHCreationCannotInjectCompletionResult(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		op := prepareNodeOperation(controlplane.NodeOperation{ID: "injected", ActionID: "injected", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "web", ResourceUID: "node", Generation: 1, LeaseEpoch: 1, Operation: "guest_ssh_access_delete", SSHResult: &substrate.SSHAccessResult{}})
		require.Error(t, store.CreateNodeOperation(ctx, op))
		_, err := store.GetNodeOperation(ctx, op.ID)
		require.Error(t, err)
		cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "node_operation", TopologyUID: "owner", ActionID: op.ActionID, ResourceUID: op.ResourceUID, Generation: op.Generation, LeaseEpoch: op.LeaseEpoch, Operation: op}
		require.Error(t, store.CreateNodeOperationDispatch(ctx, op, cmd))
		_, err = store.GetNodeOperation(ctx, op.ID)
		require.Error(t, err)
	})
}
