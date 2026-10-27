package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAdoptionDispositionRequiresFencedFreshExactOwnership(t *testing.T) {
	request, controller := fenceRequest(t)
	adopt := AdoptionRequest{ID: request.RunID, Topology: request.Topology, TopologyUID: "owner", Address: "sysbox_node.test", Provider: "libvirt", AgentID: request.Grant.AgentID, ExternalID: "immutable", ResourceUID: "resource", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: "immutable", Fingerprint: "before"}}
	request.Grant.Operation = "adoption_apply"
	request.Grant.Scope = ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	request.Grant.Digest, _ = adopt.Digest()
	request.JournalSHA256 = request.Grant.Digest
	require.NoError(t, request.Validate())
	digest, err := request.Digest()
	require.NoError(t, err)
	fence, err := SignHostFenceReceipt(controller, request, HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: request.LabID, OldBootID: request.Grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof := AdoptionDispositionProof{Request: request, Fence: fence, Adoption: adopt, HostBootID: fence.NewBootID, ObservedAt: time.Now().UTC(), Observation: substrate.NodeObservation{Exists: true, ExternalID: adopt.ExternalID, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: adopt.Generation}, Private: json.RawMessage(`{"domain_uuid":"immutable"}`)}
	signed, err := SignAdoptionDisposition(key, proof)
	require.NoError(t, err)
	require.NoError(t, VerifyAdoptionDisposition(pub, signed))
	for _, field := range []string{"boot", "time", "identity", "absent", "private", "request", "signature"} {
		bad := signed
		switch field {
		case "boot":
			bad.HostBootID = request.Grant.TargetBootID
		case "time":
			bad.ObservedAt = fence.ObservedAt.Add(-time.Second)
		case "identity":
			bad.Observation.ResourceUID = "other"
		case "absent":
			bad.Observation.Exists = false
		case "private":
			bad.Private = nil
		case "request":
			bad.Adoption.ExpectedSerial++
		case "signature":
			bad.Signature = nil
		}
		require.Error(t, VerifyAdoptionDisposition(pub, bad), field)
	}
	for _, resolution := range []string{"cancel-unowned", "cancel-absent"} {
		cancelled := proof
		cancelled.Resolution = resolution
		cancelled.Private = nil
		cancelled.Observation = substrate.NodeObservation{Exists: resolution == "cancel-unowned", ExternalID: adopt.ExternalID}
		signed, err := SignAdoptionDisposition(key, cancelled)
		require.NoError(t, err)
		require.NoError(t, VerifyAdoptionDisposition(pub, signed))
		signed.Resolution = "recover-owned"
		require.Error(t, VerifyAdoptionDisposition(pub, signed), "cancellation cannot be upgraded to success")
		cancelled.Observation.OwnerUID = "foreign"
		_, err = SignAdoptionDisposition(key, cancelled)
		require.Error(t, err, "conflicting ownership retains hold")
	}

}
