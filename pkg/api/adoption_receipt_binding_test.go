package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type adoptionCompletionFailure struct{ apiStore }

func (s adoptionCompletionFailure) CompleteNodeOperation(context.Context, string, string, controlplane.NodeOperation) (*controlplane.NodeOperation, error) {
	return nil, errors.New("injected completion failure")
}

func TestAdoptionCompletionRejectsTamperedEvidenceBeforeStateWrite(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) { testAdoptionReceiptBinding(t, store) })
}

func testAdoptionReceiptBinding(t *testing.T, store apiStore) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.apiStore = store
	s.nodeOps = newNodeOperationStore(store)
	s.nodeService = nil
	ctx := context.Background()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	req := controlplane.AdoptionRequest{ID: "adopt", Topology: "lab", TopologyUID: st.TopologyUID, Address: "sysbox_node.web", Provider: "docker", AgentID: "agent", ExternalID: "external", ResourceUID: "resource", Generation: 1, ExpectedSerial: st.Meta.Serial, Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "fingerprint"}}
	op := prepareNodeOperation(controlplane.NodeOperation{ID: "adopt", Topology: "lab", Node: "web", AgentID: "agent", Operation: "adoption_apply", ResourceUID: "resource", Generation: 1, Adoption: &req})
	op, err = (&NodeOperationService{operations: newNodeOperationStore(s.apiStore)}).enqueue(ctx, st.TopologyUID, op)
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "agent", op.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "agent", "incarnation", "")
	require.NoError(t, err)
	digest, err := req.Digest()
	require.NoError(t, err)
	g, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: uint64(cmd.Attempt), Operation: "adoption_apply", Digest: digest, TargetBootID: "host", Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID, ResourceUID: "resource", Generation: 1}}, time.Minute)
	require.NoError(t, err)
	original := json.RawMessage(`{"container_name":"original"}`)
	receipt, err := controlplane.SignAdoptionStopReceipt(key, *g, "host", "receipt", original, time.Now())
	require.NoError(t, err)
	post := func(raw json.RawMessage) *httptest.ResponseRecorder {
		body, err := json.Marshal(HostNodeOperationCompletion{Receipt: receipt, Evidence: raw})
		require.NoError(t, err)
		r := httptest.NewRecorder()
		s.mux.ServeHTTP(r, httptest.NewRequest("POST", "/v1/hosts/agent/node-operations/adopt/completed", bytes.NewReader(body)))
		return r
	}
	r := post(json.RawMessage(`{"container_name":"forged"}`))
	require.Equal(t, 409, r.Code, r.Body.String())
	validReceipt := receipt
	unissued := *g
	unissued.Epoch++
	receipt, err = controlplane.SignAdoptionStopReceipt(key, unissued, "host", "unissued", original, time.Now())
	require.NoError(t, err)
	r = post(original)
	require.Equal(t, 409, r.Code, "even a host-signed receipt cannot invent an issued epoch: %s", r.Body.String())
	receipt = validReceipt
	stopBody, err := json.Marshal(receipt)
	require.NoError(t, err)
	stopResponse := httptest.NewRecorder()
	s.mux.ServeHTTP(stopResponse, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(stopBody)))
	require.Equal(t, 409, stopResponse.Code, "adoption cannot retire before canonical completion")
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Empty(t, after.Resources)
	require.Equal(t, st.Meta.Serial, after.Meta.Serial)
	// Simulate a failure between canonical evidence commit and operation
	// completion: the old scope must remain held until the retry succeeds.
	s.nodeService = &NodeOperationService{operations: newNodeOperationStore(adoptionCompletionFailure{store})}
	r = post(original)
	require.Equal(t, 409, r.Code, r.Body.String())
	require.NoError(t, authority.Authorize(ctx, *g))
	blocked := *g
	blocked.CommandID = "blocked"
	blocked.ActionID = "blocked"
	_, err = authority.Issue(ctx, blocked, time.Minute)
	require.ErrorIs(t, err, persistence.ErrConflict)
	s.nodeService = nil
	r = post(original)
	require.Equal(t, 200, r.Code, r.Body.String())
	r = post(original)
	require.Equal(t, 200, r.Code, r.Body.String())
	after, err = repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Len(t, after.Resources, 1)
	require.JSONEq(t, string(original), string(after.Resources[0].Private))
	require.Error(t, authority.Authorize(ctx, *g), "completed adoption must retire authority")
	nextRequest := *g
	nextRequest.CommandID = "successor-command"
	nextRequest.ActionID = "successor-action"
	next, err := authority.Issue(ctx, nextRequest, time.Minute)
	require.NoError(t, err)
	require.Greater(t, next.Epoch, g.Epoch)
	r = post(original)
	require.Equal(t, 200, r.Code, r.Body.String())
	require.NoError(t, authority.Authorize(ctx, *next), "old receipt replay must not retire successor")
}
