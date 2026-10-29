package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestAdoptionDispositionAuthorityPreservesUnknownAndRequiresDecision(t *testing.T) {
	for _, mode := range []string{"recover-owned", "cancel-unowned", "cancel-absent"} {
		t.Run(mode, func(t *testing.T) { testAdoptionDispositionWithAction(t, mode) })
	}
}
func testAdoptionDispositionWithAction(t *testing.T, mode string) {
	ctx := context.Background()
	root := t.TempDir()
	authority := NewFilesystemExecutionGrantRepository(root)
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	adopt := controlplane.AdoptionRequest{ID: "adopt", Topology: "lab", TopologyUID: refs.TopologyUID, Address: "sysbox_node.test", Provider: "libvirt", AgentID: "agent", ExternalID: "external", ResourceUID: "resource", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: "external", Fingerprint: "before"}}
	action := runtime.ActionRecord{ID: "old-action", RunID: adopt.ID, Topology: "lab", Resource: address.Resource("sysbox_node", "test"), Action: model.PlanActionAdopt, Status: runtime.ActionIntentCommitted, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1, DesiredDigest: "original", Candidate: &state.Resource{Address: address.Resource("sysbox_node", "test"), Driver: "libvirt", ExternalID: adopt.ExternalID, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1}}
	require.NoError(t, repo.CommitActionIntent(ctx, action))
	original, err := repo.LoadActions(ctx, "lab")
	require.NoError(t, err)
	session, err := authority.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	digest, err := adopt.Digest()
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: "adopt", Attempt: 1, Operation: "adoption_apply", Digest: digest, TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: adopt.Generation}}, time.Minute)
	require.NoError(t, err)
	controllerPub, controller, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: "adopt", LabID: "isolated", ControllerKey: controllerPub, JournalSHA256: digest, Grant: *grant}
	_, err = authority.BeginHostFence(ctx, req)
	require.NoError(t, err)
	reqDigest, err := req.Digest()
	require.NoError(t, err)
	fence, err := controlplane.SignHostFenceReceipt(controller, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: reqDigest, LabID: req.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof, err := controlplane.SignAdoptionDisposition(key, controlplane.AdoptionDispositionProof{Request: req, Fence: fence, Adoption: adopt, HostBootID: fence.NewBootID, ObservedAt: time.Now().UTC(), Observation: substrate.NodeObservation{Exists: true, ExternalID: adopt.ExternalID, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1}, Private: json.RawMessage(`{"domain_uuid":"external"}`)})
	require.NoError(t, err)
	if mode != "recover-owned" {
		proof.Resolution = mode
		proof.Private = nil
		proof.Observation = substrate.NodeObservation{ExternalID: adopt.ExternalID, Exists: mode == "cancel-unowned"}
		proof, err = controlplane.SignAdoptionDisposition(key, proof)
		require.NoError(t, err)
	}
	require.Error(t, authority.PrepareAdoptionDisposition(ctx, proof, pub), "unrecorded reboot cannot prepare")
	require.NoError(t, authority.RecordHostFence(ctx, req.ID, fence))
	require.NoError(t, authority.PrepareAdoptionDisposition(ctx, proof, pub))
	require.Error(t, authority.FinishAdoptionDisposition(ctx, proof, "not-committed"))
	rawAuthority, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.CommitFencedAdoption(ctx, proof, pub, nil, rawAuthority))
	recovered, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	if mode == "recover-owned" {
		require.Len(t, recovered.Resources, 1)
	} else {
		require.Empty(t, recovered.Resources)
	}
	require.Equal(t, int64(1), recovered.Meta.Serial)
	require.NoError(t, repo.CommitFencedAdoption(ctx, proof, pub, nil, rawAuthority))
	again, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, recovered.Meta.Serial, again.Meta.Serial)
	actions, err := repo.LoadActions(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, original[0].Status, actions[0].Status)
	require.NotEmpty(t, actions[0].DispositionID)
	require.False(t, actions[0].EffectComplete)
	manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
	require.NoError(t, manager.Save(recovered), "settled unknown must not block new state admission")
	require.Error(t, repo.CommitActionOutcome(ctx, original[0], nil), "old journal cannot resume")
	_, err = repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)

	require.NoError(t, authority.FinishAdoptionDisposition(ctx, proof, AdoptionDispositionHash(proof)))
	cold := NewFilesystemExecutionGrantRepository(root)
	require.NoError(t, cold.FinishAdoptionDisposition(ctx, proof, AdoptionDispositionHash(proof)))
	require.Error(t, cold.Authorize(ctx, *grant))
	stored, err := cold.ReadHostFence(ctx, req.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.AdoptionDisposition)
	require.Equal(t, "unknown", stored.AdoptionDisposition.OperationResult)
	require.True(t, stored.released())
}
