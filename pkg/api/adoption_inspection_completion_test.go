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
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestInspectionHostCompletionRejectsForgedEvidenceAndColdReplays(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys["agent"] = pub
		req := controlplane.AdoptionInspectionRequest{ID: "inspect", Topology: "lab", TopologyUID: st.TopologyUID, Address: "sysbox_node.web", Provider: "libvirt", AgentID: "agent", ExternalID: "domain-name", ResourceUID: "provisional", Generation: 1}
		op := prepareNodeOperation(controlplane.NodeOperation{ID: req.ID, Topology: "lab", Node: "web", AgentID: "agent", Operation: "adoption_inspect", ResourceUID: req.ResourceUID, Generation: 1, Inspection: &req})
		op, err = (&NodeOperationService{operations: newNodeOperationStore(store)}).enqueue(ctx, st.TopologyUID, op)
		require.NoError(t, err)
		cmd, ok, err := store.AcquireAgentCommandLease(ctx, "agent", op.CommandID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"}
		g, err := s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.NoError(t, err)
		managed := substrate.OperationIdentity{ActionID: "original", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "original-resource", Generation: 7}
		evidence := substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: "domain-uuid", Fingerprint: "sha256:hash", State: json.RawMessage(`{"z":1,"a":2}`), ManagedIdentity: &managed}
		raw, err := json.Marshal(controlplane.AdoptionInspectionResult{Evidence: &evidence})
		require.NoError(t, err)
		receipt, err := controlplane.SignAdoptionStopReceipt(key, *g, "boot", "receipt", raw, time.Now())
		require.NoError(t, err)
		post := func(raw json.RawMessage) *httptest.ResponseRecorder {
			body, err := json.Marshal(HostNodeOperationCompletion{Receipt: receipt, Evidence: raw})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/node-operations/inspect/completed", bytes.NewReader(body)))
			return w
		}
		require.Equal(t, 409, post(json.RawMessage(`{"error":"forged"}`)).Code)
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID), "host result survives worker retirement")
		response := post(raw)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Equal(t, 200, post(raw).Code)
		completed, err := store.GetNodeOperation(ctx, "inspect")
		require.NoError(t, err)
		require.NotNil(t, completed.InspectionEvidence)
		require.JSONEq(t, string(evidence.State), string(completed.InspectionEvidence.State))
		require.Equal(t, evidence.ManagedIdentity, completed.InspectionEvidence.ManagedIdentity)
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, st, after)
		_, err = s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.Error(t, err)
	})
}
