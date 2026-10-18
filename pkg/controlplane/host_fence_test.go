package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fenceRequest(t *testing.T) (HostFenceRequest, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return HostFenceRequest{Version: 1, ID: "fence-1", Topology: "lab", RunID: "run", LabID: "disposable-vm", ControllerKey: pub, JournalSHA256: strings.Repeat("a", 64), Grant: ExecutionGrant{AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "run", Attempt: 1, Epoch: 1, Operation: "run_destroy", Digest: strings.Repeat("b", 64), Scope: ExecutionScope{TopologyUID: "owner"}, TargetBootID: "11111111-1111-4111-8111-111111111111", ExpiresAt: time.Now().UTC().Add(time.Minute), State: GrantActive}}, key
}
func TestHostFenceReceiptBindsRequestAndCannotClaimSuccess(t *testing.T) {
	req, key := fenceRequest(t)
	digest, err := req.Digest()
	require.NoError(t, err)
	receipt := HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: req.LabID, OldBootID: req.Grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()}
	signed, err := SignHostFenceReceipt(key, req, receipt)
	require.NoError(t, err)
	require.NoError(t, VerifyHostFenceReceipt(req, signed))
	for _, field := range []string{"digest", "boot", "result", "pid", "controller", "scope", "signature"} {
		t.Run(field, func(t *testing.T) {
			bad := signed
			switch field {
			case "digest":
				bad.RequestSHA256 = strings.Repeat("c", 64)
			case "boot":
				bad.NewBootID = bad.OldBootID
			case "result":
				bad.OperationResult = "succeeded"
			case "pid":
				bad.QEMUPID++
			case "controller":
				bad.ControllerBootUnchanged = false
			case "scope":
				bad.ProviderScope = "remote"
			case "signature":
				bad.Signature = nil
			}
			require.Error(t, VerifyHostFenceReceipt(req, bad))
		})
	}
	other := req
	other.Grant.Epoch++
	require.Error(t, VerifyHostFenceReceipt(other, signed))
	other = req
	other.JournalSHA256 = strings.Repeat("d", 64)
	require.Error(t, VerifyHostFenceReceipt(other, signed))
}
