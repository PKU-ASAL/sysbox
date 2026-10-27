package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAdoptionHostCompletionDoesNotBorrowWorkerLease(t *testing.T) {
	req := controlplane.AdoptionRequest{ID: "adopt", AgentID: "agent", Topology: "lab", TopologyUID: "uid", Address: "sysbox_node.web", Provider: "docker", ExternalID: "external", ResourceUID: "node", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "proof"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	op := controlplane.NodeOperation{ID: "adopt", AgentID: "agent", CommandID: "cmd", Topology: "lab", Operation: "adoption_apply", ActionID: "adopt", ResourceUID: "node", Generation: 1, Adoption: &req, Status: controlplane.NodeOperationStatusQueued}
	cmd := &CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: "cmd", Type: "node_operation", AgentID: "agent", TopologyUID: "uid", ActionID: "adopt", ResourceUID: "node", Generation: 1, Operation: op, Attempt: 2, LeaseUntil: time.Now().Add(-time.Hour)}}
	r := controlplane.SignedExecutionStopReceipt{ReceiptID: "receipt", EvidenceDigest: "proof", StoppedAt: time.Now().UTC(), Grant: controlplane.ExecutionGrant{AgentID: "agent", CommandID: "cmd", ActionID: "adopt", Attempt: 1, Operation: "adoption_apply", Digest: digest, Scope: controlplane.ExecutionScope{TopologyUID: "uid", ResourceUID: "node", Generation: 1}}}
	done, err := CompleteAdoptionFromHost(op, cmd, "agent", "adopt", r)
	require.NoError(t, err)
	require.Equal(t, controlplane.NodeOperationStatusDone, done.Status)
	require.Empty(t, done.LeaseOwner)
	require.Zero(t, done.Attempt)
	_, err = CompleteNodeOperation(op, cmd, "agent", "adopt", done, time.Now())
	require.ErrorIs(t, err, ErrConflict, "ordinary worker completion cannot claim a host result")
	_, err = CompleteAdoptionFromHost(done, cmd, "agent", "adopt", r)
	require.NoError(t, err)
	r.Grant.Digest = "different"
	_, err = CompleteAdoptionFromHost(op, cmd, "agent", "adopt", r)
	require.ErrorIs(t, err, ErrConflict)
}
