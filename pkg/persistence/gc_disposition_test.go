package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestGCDispositionRetainsUnknownAndReleasesOnlyAfterFencing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	authority := NewFilesystemExecutionGrantRepository(root)
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	c := controlplane.GCCandidate{Kind: "domain", ExternalID: "external", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "resource", Generation: 1, InventoryDigest: refs.Digest}
	reservation := GCReservation{ID: "gc", AgentID: "agent", Candidate: c}
	require.NoError(t, repo.ReserveGC(ctx, "lab", reservation))
	session, err := authority.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	digest, err := c.Digest()
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "gc", ActionID: "gc", Attempt: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation}}, time.Minute)
	require.NoError(t, err)
	controllerPub, controller, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: "gc", LabID: "isolated", ControllerKey: controllerPub, JournalSHA256: evidenceHash(reservation), Grant: *grant}
	_, err = authority.BeginHostFence(ctx, req)
	require.NoError(t, err)
	reqDigest, err := req.Digest()
	require.NoError(t, err)
	fence, err := controlplane.SignHostFenceReceipt(controller, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: reqDigest, LabID: req.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof, err := controlplane.SignGCDisposition(key, controlplane.GCDispositionProof{Request: req, Fence: fence, Candidate: c, HostBootID: fence.NewBootID, Presence: "absent", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.Error(t, authority.PrepareGCDisposition(ctx, proof, pub), "unrecorded reboot cannot prepare")
	require.NoError(t, authority.RecordHostFence(ctx, req.ID, fence))
	require.NoError(t, authority.PrepareGCDisposition(ctx, proof, pub))
	stored, err := repo.LoadGCReservation(ctx, "lab", "gc")
	require.NoError(t, err)
	require.True(t, stored.Pending())
	require.NoError(t, repo.CompleteGCDisposition(ctx, "lab", proof, pub))
	stored, err = repo.LoadGCReservation(ctx, "lab", "gc")
	require.NoError(t, err)
	require.False(t, stored.Pending())
	require.Empty(t, stored.ReceiptHash, "do not invent deletion success")
	cold := NewFilesystemExecutionGrantRepository(root)
	require.NoError(t, cold.PrepareGCDisposition(ctx, proof, pub))
	require.NoError(t, cold.FinishGCDisposition(ctx, proof, stored.DispositionHash))
	require.NoError(t, cold.FinishGCDisposition(ctx, proof, stored.DispositionHash))
	require.Error(t, cold.Authorize(ctx, *grant))
	next := *grant
	next.Epoch += 2
	next.CommandID = "fresh"
	next.TargetBootID = "44444444-4444-4444-8444-444444444444"
	freshCandidate := c
	freshCandidate.InventoryDigest = "new"
	next.Digest, _ = freshCandidate.Digest()
	// An absent historical target does not authorize residue deletion.
	selected, err := cold.ReleasedGCDisposition(ctx, freshCandidate, next, grant.TargetBootID)
	require.NoError(t, err)
	require.Nil(t, selected)

	changed := proof
	changed.Presence = "present"
	changed, err = controlplane.SignGCDisposition(key, changed)
	require.NoError(t, err)
	require.Error(t, cold.PrepareGCDisposition(ctx, changed, pub))
	require.Error(t, repo.CompleteGCDisposition(ctx, "lab", changed, pub))
}

func TestReleasedGCProofSelectsManifestBootAcrossLaterReboots(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := NewFilesystemExecutionGrantRepository(root)
	// Reuse a fully exercised disposition fixture by testing selection's rejection
	// independently: no proof is synthesized for a missing historical fence.
	c := controlplane.GCCandidate{Kind: "firecracker", ExternalID: "vm", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "fresh"}
	digest, _ := c.Digest()
	g := controlplane.ExecutionGrant{Operation: "gc_delete", Digest: digest, AgentID: "agent", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, Epoch: 3, CommandID: "fresh", TargetBootID: "new"}
	p, err := repo.ReleasedGCDisposition(ctx, c, g, "old")
	require.NoError(t, err)
	require.Nil(t, p)
	g.Digest = "other"
	_, err = repo.ReleasedGCDisposition(ctx, c, g, "old")
	require.Error(t, err)
}
