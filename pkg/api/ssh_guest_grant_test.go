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

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHGrantUsesDurableMutationAndLease(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 4, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	op := controlplane.NodeOperation{ID: req.ID, ActionID: req.ID, AgentID: "agent", Topology: "lab", Node: "web", ResourceUID: "resource", Generation: 3, Operation: "guest_ssh_access_delete", Status: controlplane.NodeOperationStatusQueued, SSHMutation: &req}
	cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ActionID: req.ID, ResourceUID: op.ResourceUID, Generation: 3, Operation: op, Status: controlplane.AgentCommandStatusLeased, Attempt: 2, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute)}
	proof := ExecutionGrantRequest{SessionID: "session", Attempt: 2, LeaseOwner: "worker", BootID: "guest-boot"}
	g, err := s.sourceGrant(context.Background(), "agent", proof, &persistence.CommandRecord{Command: cmd})
	require.NoError(t, err)
	digest, err := req.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, g.Digest)
	require.Equal(t, op.Operation, g.Operation)
	require.Equal(t, "guest-boot", g.TargetBootID)
	require.Equal(t, cmd.LeaseUntil, g.ExpiresAt)
	require.Equal(t, controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 3}, g.Scope)
	for _, field := range []string{"retired", "attempt", "lease", "expired", "mutation"} {
		bad := persistence.CommandRecord{Command: cmd}
		p := proof
		switch field {
		case "retired":
			bad.Retired = true
		case "attempt":
			p.Attempt++
		case "lease":
			p.LeaseOwner = "other"
		case "expired":
			bad.Command.LeaseUntil = time.Now().Add(-time.Second)
		case "mutation":
			bad.Command.Operation.SSHMutation = nil
		}
		_, err := s.sourceGrant(context.Background(), "agent", p, &bad)
		require.Error(t, err, field)
	}
}

func TestGuestSSHGrantHTTPBindsAcceptedCASAndRejectsPayloadOverride(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "session", "")
		require.NoError(t, err)
		mutation := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 4, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
		op := prepareNodeOperation(controlplane.NodeOperation{ID: mutation.ID, ActionID: mutation.ID, AgentID: "agent", Topology: "lab", Node: "web", ResourceUID: "resource", Generation: 3, Operation: "guest_ssh_access_delete", SSHMutation: &mutation})
		command := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: grantTestTopologyUID(t, s, "lab"), ResourceUID: op.ResourceUID, Generation: op.Generation, ActionID: op.ID, Operation: op}
		require.NoError(t, store.SaveAgentCommand(ctx, command))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", command.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		post := func(path string, body any) *httptest.ResponseRecorder {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
			require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, r)
			return w
		}
		path := "/v1/agents/agent/commands/command/execution-grant"
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: leased.LeaseOwner, Attempt: leased.Attempt, BootID: "guest-boot"}
		require.Equal(t, 409, post(path, proof).Code, "a command snapshot alone cannot authorize a missing operation")
		op.CommandID = command.ID
		require.NoError(t, store.CreateNodeOperation(ctx, op))
		response := post(path, proof)
		require.Equal(t, 200, response.Code, response.Body.String())
		var signed controlplane.SignedExecutionGrant
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &signed))
		require.NoError(t, controlplane.VerifyExecutionGrant(pub, signed, "guest-boot", time.Now()))
		digest, err := mutation.Digest()
		require.NoError(t, err)
		require.Equal(t, digest, signed.Grant.Digest)
		require.Equal(t, "guest_ssh_access_delete", signed.Grant.Operation)
		require.Equal(t, 200, post("/v1/agents/agent/execution-grants/authorize", signed).Code)
		// Even an otherwise correctly signed envelope cannot change the
		// accepted CAS request when online authority revalidates its source.
		changed := mutation
		changed.ExpectedRevision++
		forgedGrant := signed.Grant
		forgedGrant.Digest, err = changed.Digest()
		require.NoError(t, err)
		forged, err := controlplane.SignExecutionGrant(key, forgedGrant, "guest-boot", time.Now())
		require.NoError(t, err)
		require.Equal(t, 409, post("/v1/agents/agent/execution-grants/authorize", forged).Code)
		require.Equal(t, 400, post(path, map[string]any{"session_id": session.ID, "lease_owner": leased.LeaseOwner, "attempt": leased.Attempt, "boot_id": "guest-boot", "ssh_mutation": changed}).Code)
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", command.ID))
		require.NotEqual(t, 200, post(path, proof).Code)
	})
}
