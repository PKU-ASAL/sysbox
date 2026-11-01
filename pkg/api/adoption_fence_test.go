package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAdoptionFenceRecoversObservedOwnerWithoutInventingSuccess(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) { testAdoptionFenceRecovery(t, failed, "") })
	}
}
func TestAdoptionFenceCancellationClosesUnknownActions(t *testing.T) {
	for _, mode := range []string{"cancel-unowned", "cancel-absent"} {
		t.Run(mode, func(t *testing.T) { testAdoptionFenceRecovery(t, true, mode) })
	}
}
func testAdoptionFenceRecovery(t *testing.T, failed bool, mode string) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		if _, ok := store.(*controlstore.SQLite); ok {
			s.stateBackend += "?topology={topology}"
		}
		issuerPub, issuer, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
		host, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"host": host}
		controllerPub, controller, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.fenceTargets = map[string]hostFenceTarget{"host": {LabID: "isolated", PublicKey: controllerPub}}
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "host", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		adopt := controlplane.AdoptionRequest{ID: "adopt", Topology: "lab", TopologyUID: st.TopologyUID, Address: "sysbox_node.web", Provider: "libvirt", AgentID: "host", ExternalID: "immutable", ResourceUID: "node", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: "immutable", Fingerprint: "before"}}
		op := prepareNodeOperation(controlplane.NodeOperation{ID: adopt.ID, Topology: "lab", Node: "web", AgentID: "host", Operation: "adoption_apply", ResourceUID: "node", Generation: 1, Adoption: &adopt})
		op, err = (&NodeOperationService{operations: newNodeOperationStore(store)}).enqueue(ctx, st.TopologyUID, op)
		require.NoError(t, err)
		cmd, ok, err := store.AcquireAgentCommandLease(ctx, "host", op.CommandID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "host", "process", "")
		require.NoError(t, err)
		grant, err := s.issueCommandGrant(ctx, "host", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "11111111-1111-4111-8111-111111111111"})
		require.NoError(t, err)
		signed, err := controlplane.SignExecutionGrant(issuer, *grant, grant.TargetBootID, time.Now())
		require.NoError(t, err)
		ledger := t.TempDir()
		before := hostsupervisor.AdoptionCoordinator{Root: ledger, IssuerKey: issuerPub, HostKey: key, AgentID: "host", HostBootID: grant.TargetBootID, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
			t.Fatal("unknown may not report success")
			return nil
		}}
		_, err = before.Apply(ctx, adopt, signed, func(context.Context) (json.RawMessage, error) {
			return nil, fmt.Errorf("provider mutated, callback return lost")
		})
		require.Error(t, err)
		if mode != "" {
			action := runtime.ActionRecord{ID: "unknown-adoption", RunID: adopt.ID, Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionAdopt, Status: runtime.ActionIntentCommitted, DesiredDigest: "original", OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: adopt.Provider, ExternalID: adopt.ExternalID, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1}}
			require.NoError(t, repo.CommitActionIntent(ctx, action))
		}
		if failed {
			current, err := store.GetNodeOperation(ctx, adopt.ID)
			require.NoError(t, err)
			next := *current
			next.Status = controlplane.NodeOperationStatusFailed
			next.Err = "original provider timeout"
			next.EndedAt = time.Now().UTC()
			require.NoError(t, store.CompareAndSwapNodeOperation(ctx, *current, next))
		}
		body, _ := json.Marshal(map[string]any{"grant": grant})
		req := httptest.NewRequest("POST", "/v1/topologies/lab/adoptions/adopt/host-fences", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "recover")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, 202, w.Code, w.Body.String())
		var record persistence.HostFenceRecord
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &record))
		digest, err := record.Request.Digest()
		require.NoError(t, err)
		fence, err := controlplane.SignHostFenceReceipt(controller, record.Request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: "isolated", OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
		require.NoError(t, err)
		require.NoError(t, authority.RecordHostFence(ctx, record.Request.ID, fence))
		server := httptest.NewServer(s)
		defer server.Close()
		client := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "host", Secret: "secret", APIURL: server.URL}}
		after := hostsupervisor.AdoptionCoordinator{Root: ledger, IssuerKey: issuerPub, HostKey: key, AgentID: "host", HostBootID: fence.NewBootID, AuthorizeFence: client.AuthorizeAdoptionFence, InspectFencedAdoption: func(context.Context, controlplane.AdoptionRequest) (substrate.NodeObservation, json.RawMessage, error) {
			return substrate.NodeObservation{Exists: true, ExternalID: adopt.ExternalID, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1}, json.RawMessage(`{"domain_uuid":"immutable"}`), nil
		}}
		var proof controlplane.AdoptionDispositionProof
		if mode != "" {
			after.InspectAdoptionCancellation = func(context.Context, controlplane.AdoptionDispositionProof) (substrate.NodeObservation, error) {
				return substrate.NodeObservation{ExternalID: adopt.ExternalID, Exists: mode == "cancel-unowned"}, nil
			}
			proof, err = after.CancelDisposition(ctx, record.Request.ID)
		} else {
			proof, err = after.Disposition(ctx, record.Request.ID)
		}
		require.NoError(t, err)
		require.NoError(t, authority.PrepareAdoptionDisposition(ctx, proof, host))
		require.Error(t, s.commitAdoptionState(ctx, op, proof.Private), "late normal commit cannot race prepared recovery")

		for range 2 {
			raw, _ := json.Marshal(proof)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/topologies/lab/host-fences/"+record.Request.ID+"/reconcile-adoption", bytes.NewReader(raw)))
			require.Equal(t, 200, w.Code, w.Body.String())
		}
		recovered, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		if mode == "" {
			require.Len(t, recovered.Resources, 1)
		} else {
			require.Empty(t, recovered.Resources)
			actions, err := repo.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, actions, 1)
			require.NotEmpty(t, actions[0].DispositionID)
			require.Equal(t, runtime.ActionIntentCommitted, actions[0].Status)
			next := actions[0]
			next.ID = "new-action"
			next.RunID = "new-run"
			next.DispositionID = ""
			next.Revision = 0
			next.ExpectedSerial = recovered.Meta.Serial
			next.StateSerial = recovered.Meta.Serial
			require.NoError(t, repo.CommitActionIntent(ctx, next), "cancelled unknown no longer blocks new request")
		}
		require.Equal(t, int64(1), recovered.Meta.Serial)
		ended, err := store.GetNodeOperation(ctx, adopt.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.NodeOperationStatusFailed, ended.Status)
		if failed {
			require.Equal(t, "original provider timeout", ended.Err)
		} else {
			require.Contains(t, ended.Err, "original result unknown")
		}
		require.Error(t, authority.Authorize(ctx, *grant))
		record, err = authority.ReadHostFence(ctx, record.Request.ID)
		require.NoError(t, err)
		require.False(t, record.AdoptionDisposition.ReleasedAt.IsZero())
		late, err := controlplane.SignAdoptionStopReceipt(key, *grant, grant.TargetBootID, "late", json.RawMessage(`{"domain_uuid":"immutable"}`), time.Now())
		require.NoError(t, err)
		raw, _ := json.Marshal(HostNodeOperationCompletion{Receipt: late, Evidence: json.RawMessage(`{"domain_uuid":"immutable"}`)})
		w = httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/host/node-operations/adopt/completed", bytes.NewReader(raw)))
		require.Equal(t, 409, w.Code, w.Body.String())
	})
}

func testFencedTransferCompletion(t *testing.T, s *Server, authority *persistence.ExecutionGrantRepository, op controlplane.NodeOperation, g controlplane.ExecutionGrant, hostKey ed25519.PrivateKey, private json.RawMessage, phase string) {
	ctx := context.Background()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, err := op.Adoption.Digest()
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "transfer-fence", Topology: op.Topology, RunID: op.ID, LabID: "isolated", ControllerKey: pub, JournalSHA256: digest, Grant: g}
	_, err = authority.BeginHostFence(ctx, req)
	require.NoError(t, err)
	sum, err := req.Digest()
	require.NoError(t, err)
	receipt, err := controlplane.SignHostFenceReceipt(key, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: sum, LabID: req.LabID, OldBootID: g.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, authority.RecordHostFence(ctx, req.ID, receipt))
	p, err := controlplane.SignAdoptionDisposition(hostKey, controlplane.AdoptionDispositionProof{Request: req, Fence: receipt, Adoption: *op.Adoption, HostBootID: receipt.NewBootID, ObservedAt: time.Now().UTC(), Observation: substrate.NodeObservation{Exists: true, ExternalID: op.Adoption.ExternalID, OwnerUID: op.Adoption.TopologyUID, ResourceUID: op.Adoption.ResourceUID, Generation: op.Adoption.Generation}, Private: private})
	require.NoError(t, err)
	if phase != "fenced-recovery" {
		p.Resolution = "cancel-absent"
		p.Private = nil
		p.Observation = substrate.NodeObservation{ExternalID: op.Adoption.ExternalID}
		if phase == "fenced-cancel-source" {
			source := op.Adoption.Transfer.Plan.Source
			p.Resolution = "cancel-source"
			p.Observation.Exists = true
			p.Observation.OwnerUID = source.OwnerUID
			p.Observation.ResourceUID = source.ResourceUID
			p.Observation.Generation = source.Generation
		}
		p, err = controlplane.SignAdoptionDisposition(hostKey, p)
		require.NoError(t, err)
	}
	require.NoError(t, s.reconcileAdoptionFence(ctx, p))
	require.NoError(t, s.reconcileAdoptionFence(ctx, p))
	repo, err := s.actionRepository(op.Topology)
	require.NoError(t, err)
	state, err := repo.LoadState(ctx, op.Topology)
	require.NoError(t, err)
	if phase == "fenced-recovery" {
		require.Len(t, state.Resources, 1)
	} else {
		require.Empty(t, state.Resources)
	}
	require.Equal(t, op.Adoption.ExpectedSerial+1, state.Meta.Serial)
	ended, err := s.apiStore.GetNodeOperation(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.NodeOperationStatusFailed, ended.Status)
}
