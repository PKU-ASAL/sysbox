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
	"github.com/stretchr/testify/require"
)

func TestNodePowerGrantBindsPersistedOperationAndLiveSession(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
		repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := repo.OpenSession(ctx, "agent", "first", "")
		require.NoError(t, err)
		op, command := nodeDispatchFixture()
		command.TopologyUID = grantTestTopologyUID(t, s, op.Topology)
		require.NoError(t, store.CreateNodeOperationDispatch(ctx, op, command))
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
		hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": hostPub}
		receipt, err := controlplane.SignExecutionStopReceipt(hostKey, signed.Grant, "host-boot", "power-done", time.Now())
		require.NoError(t, err)
		response = post("/v1/hosts/agent/execution-grants/stopped", receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		done, err := store.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.NodeOperationStatusDone, done.Status)
		// A replay after command retirement must not lose the canonical result.
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", command.ID))
		response = post("/v1/hosts/agent/execution-grants/stopped", receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
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

func TestPowerHostCompletionSurvivesDeliveryLeaseReplacement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		op, cmd := nodeDispatchFixture()
		require.NoError(t, store.CreateNodeOperationDispatch(ctx, op, cmd))
		old, ok, err := store.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "first", time.Nanosecond)
		require.NoError(t, err)
		require.True(t, ok)
		repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := repo.OpenSession(ctx, cmd.AgentID, "process", "")
		require.NoError(t, err)
		req, err := controlplane.FromNodeOperationCommand(*old)
		require.NoError(t, err)
		digest, err := req.Digest()
		require.NoError(t, err)
		grant, err := repo.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: op.ResourceUID, Generation: op.Generation}, AgentID: cmd.AgentID, SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: uint64(old.Attempt), Operation: "node_power", Digest: digest, TargetBootID: "host"}, time.Minute)
		require.NoError(t, err)
		receipt, err := controlplane.SignExecutionStopReceipt(key, *grant, "host", "receipt", time.Now().UTC())
		require.NoError(t, err)
		fresh, ok, err := store.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "replacement", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.Greater(t, fresh.Attempt, old.Attempt)
		raw, err := json.Marshal(receipt)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
		require.Equal(t, 200, w.Code, w.Body.String())
		observed, err := store.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.NodeOperationStatusDone, observed.Status)
	})
}
