package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNodePowerGrantBindsPersistedOperationAndLiveSession(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		s.agentSvc = newAgentService(s)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
		repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := repo.OpenSession(ctx, "agent", "first", "")
		require.NoError(t, err)
		command := controlplane.AgentCommand{ID: "power-command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ResourceUID: "resource", Generation: 2, ActionID: "action", Operation: controlplane.NodeOperation{Topology: "lab", Node: "vm", Operation: "pause"}}
		require.NoError(t, store.SaveAgentCommand(ctx, command))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", command.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		post := func(path string, value any) *httptest.ResponseRecorder {
			raw, e := json.Marshal(value)
			require.NoError(t, e)
			request := httptest.NewRequest("POST", path, bytes.NewReader(raw))
			require.NoError(t, agent.SignRequest(request, "agent", "secret", time.Now()))
			response := httptest.NewRecorder()
			s.mux.ServeHTTP(response, request)
			return response
		}
		path := "/v1/agents/agent/commands/" + command.ID + "/execution-grant"
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: leased.LeaseOwner, Attempt: leased.Attempt, BootID: "host-boot"}
		response := post(path, proof)
		require.Equal(t, 200, response.Code, response.Body.String())
		var signed controlplane.SignedExecutionGrant
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &signed))
		require.NoError(t, controlplane.VerifyExecutionGrant(pub, signed, "host-boot", time.Now()))
		require.Equal(t, "node_power", signed.Grant.Operation)
		power, err := controlplane.FromNodeOperationCommand(*leased)
		require.NoError(t, err)
		digest, err := power.Digest()
		require.NoError(t, err)
		require.Equal(t, digest, signed.Grant.Digest)
		authorize := "/v1/agents/agent/execution-grants/authorize"
		response = post(authorize, signed)
		require.Equal(t, 200, response.Code, response.Body.String())
		for _, mutate := range []func(*controlplane.ExecutionGrant){func(g *controlplane.ExecutionGrant) { g.Scope.ResourceUID = "other" }, func(g *controlplane.ExecutionGrant) { p := power; p.Operation = "resume"; g.Digest, _ = p.Digest() }} {
			changed := signed.Grant
			mutate(&changed)
			forged, e := controlplane.SignExecutionGrant(key, changed, "host-boot", time.Now())
			require.NoError(t, e)
			response = post(authorize, forged)
			require.Equal(t, 409, response.Code, response.Body.String())
		}
		wrong := proof
		wrong.Attempt++
		response = post(path, wrong)
		require.Equal(t, 409, response.Code)
		wrong = proof
		wrong.LeaseOwner = "other"
		response = post(path, wrong)
		require.Equal(t, 409, response.Code)
		_, err = repo.OpenSession(ctx, "agent", "second", session.ID)
		require.NoError(t, err)
		response = post(authorize, signed)
		require.Equal(t, 409, response.Code)
		response = post(path+"/renew", ExecutionGrantAuthorization{Grant: signed, LeaseOwner: leased.LeaseOwner})
		require.Equal(t, 409, response.Code)
	})
}

func TestSourceGrantNeverSignsImportOrWorkerSelectedExternalID(t *testing.T) {
	s := &Server{}
	for _, op := range []controlplane.NodeOperation{{Topology: "lab", Node: "vm", Operation: "import"}, {Topology: "lab", Node: "vm", Operation: "pause", ExternalID: "foreign"}} {
		cmd := controlplane.AgentCommand{ID: "cmd", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: "action", Operation: op, Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute), Status: "running"}
		_, err := s.sourceGrant(context.Background(), "agent", ExecutionGrantRequest{SessionID: "session", LeaseOwner: "worker", Attempt: 1, BootID: "boot"}, &persistence.CommandRecord{Command: cmd})
		require.Error(t, err)
	}
}
