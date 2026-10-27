package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerOwnershipTransferPreservesStoppedArtifacts(t *testing.T) {
	s, h, m, _ := manifestFixture(t, false)
	old := substrate.OperationIdentity{ActionID: "action", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	// Fixture uses legacy owner directory; the signed transfer preserves it.
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: 2}
	_, raw, err := s.findAdoptionManifest(context.Background(), h.ID)
	require.NoError(t, err)
	p := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "firecracker", ExternalID: h.ID, Source: old, Target: next, Fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, _ := p.Digest()
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: p, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, s.InspectOwnershipTransfer(context.Background(), p))
	scoped := substrate.WithOperationIdentity(context.Background(), next)
	transferred, err := s.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	require.Equal(t, h.ID, transferred.ID)
	require.Equal(t, h.Provider.(*HandleState).VMDir, transferred.Provider.(*HandleState).VMDir)
	cold := New("", s.rootfsDir)
	again, err := cold.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	require.Equal(t, transferred.ID, again.ID)
	_, err = cold.loadManifest(substrate.WithOperationIdentity(context.Background(), old), h)
	require.Error(t, err)
	found, err := cold.DiscoverManagedNode(scoped, next, "renamed")
	require.NoError(t, err)
	require.Equal(t, h.ID, found.ID)
	_, secondRaw, err := cold.findAdoptionManifest(context.Background(), h.ID)
	require.NoError(t, err)
	final := substrate.OperationIdentity{ActionID: "transfer-again", OwnerUID: "third", TopologyUID: "third", ResourceUID: "third-resource", Generation: 3}
	second := p
	second.ID = final.ActionID
	second.Source = next
	second.Target = final
	second.SourceTopology = "target"
	second.TargetTopology = "third"
	second.Fingerprint = fmt.Sprintf("sha256:%x", sha256.Sum256(secondRaw))
	require.NoError(t, cold.InspectOwnershipTransfer(context.Background(), second))
	secondDigest, _ := second.Digest()
	secondProof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: second, PlanDigest: secondDigest, SourceEpoch: 4, SourceStateDigest: strings.Repeat("c", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	finalCtx := substrate.WithOperationIdentity(context.Background(), final)
	finalHandle, err := cold.ApplyOwnershipTransfer(finalCtx, secondProof, pub)
	require.NoError(t, err)
	require.Error(t, cold.DestroyNode(scoped, transferred))
	require.NoError(t, cold.DestroyNode(finalCtx, finalHandle))
	require.NoDirExists(t, h.Provider.(*HandleState).VMDir)
	_ = m
}
