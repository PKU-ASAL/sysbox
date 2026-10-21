package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOwnershipTransferRequiresExactRetiredSourceAndTarget(t *testing.T) {
	source := substrate.OperationIdentity{ActionID: "old", OwnerUID: "source", TopologyUID: "source", ResourceUID: "resource", Generation: 3}
	target := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new-resource", Generation: 4}
	plan := OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "old-lab", TargetTopology: "new-lab", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: "external", Source: source, Target: target, SourceSerial: 7, TargetSerial: 9, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	digest, err := plan.Digest()
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof, err := SignOwnershipRetirement(key, OwnershipRetirementProof{Plan: plan, PlanDigest: digest, SourceEpoch: 5, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, VerifyOwnershipRetirement(pub, proof))
	for _, field := range []string{"source", "target", "external", "epoch", "signature"} {
		bad := proof
		switch field {
		case "source":
			bad.Plan.Source.Generation++
		case "target":
			bad.Plan.Target.TopologyUID = "other"
		case "external":
			bad.Plan.ExternalID = "replacement"
		case "epoch":
			bad.SourceEpoch = 0
		case "signature":
			bad.Signature = nil
		}
		require.Error(t, VerifyOwnershipRetirement(pub, bad), field)
	}
	plan.Target.OwnerUID = plan.Source.OwnerUID
	_, err = plan.Digest()
	require.Error(t, err)
}
