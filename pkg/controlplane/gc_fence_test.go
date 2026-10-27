package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGCFenceRequiresBoundRebootAndFreshObservation(t *testing.T) {
	request, controller := fenceRequest(t)
	candidate := GCCandidate{Kind: "domain", ExternalID: "external", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "inventory"}
	request.Grant.Operation = "gc_delete"
	request.Grant.Scope = ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	request.Grant.CommandID = request.RunID
	request.Grant.Digest, _ = candidate.Digest()
	require.NoError(t, request.Validate())
	digest, err := request.Digest()
	require.NoError(t, err)
	fence, err := SignHostFenceReceipt(controller, request, HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: request.LabID, OldBootID: request.Grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof := GCDispositionProof{Request: request, Fence: fence, Candidate: candidate, HostBootID: fence.NewBootID, Presence: "absent", ObservedAt: time.Now().UTC()}
	signed, err := SignGCDisposition(key, proof)
	require.NoError(t, err)
	require.NoError(t, VerifyGCDisposition(pub, signed))
	for _, field := range []string{"boot", "candidate", "time", "presence", "signature", "fence"} {
		bad := signed
		switch field {
		case "boot":
			bad.HostBootID = request.Grant.TargetBootID
		case "candidate":
			bad.Candidate.Generation++
		case "time":
			bad.ObservedAt = fence.ObservedAt.Add(-time.Second)
		case "presence":
			bad.Presence = "deleted"
		case "signature":
			bad.Signature = nil
		case "fence":
			bad.Fence.Signature = nil
		}
		require.Error(t, VerifyGCDisposition(pub, bad), field)
	}
	proof.Presence = "present"
	signed, err = SignGCDisposition(key, proof)
	require.NoError(t, err)
	require.NoError(t, VerifyGCDisposition(pub, signed))
}
