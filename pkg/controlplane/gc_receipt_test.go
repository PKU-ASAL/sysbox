package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGCReceiptRequiresExactCandidateAndConfirmedAbsence(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c := GCCandidate{Kind: "container", ExternalID: "external", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "inventory"}
	digest, err := c.Digest()
	require.NoError(t, err)
	g := ExecutionGrant{Operation: "gc_delete", Digest: digest, Scope: ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation}, AgentID: "agent", SessionID: "session", CommandID: "gc", ActionID: "gc", Attempt: 1, Epoch: 1, TargetBootID: "boot"}
	receipt, err := SignGCDeletionReceipt(key, g, "boot", "receipt", c, time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyGCDeletionReceipt(pub, receipt, c))
	changed := c
	changed.ExternalID = "replacement"
	require.Error(t, VerifyGCDeletionReceipt(pub, receipt, changed))
	generic, err := SignExecutionStopReceipt(key, g, "boot", "generic", time.Now())
	require.NoError(t, err)
	require.Error(t, VerifyGCDeletionReceipt(pub, generic, c), "generic quiescence is not deletion evidence")
}
