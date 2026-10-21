package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestGCReservationFencesReferencesUntilVerifiedCompletion(t *testing.T) {
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "repo")
			open := func() *Repository {
				var r *Repository
				var err error
				if kind == "file" {
					r, err = OpenFilesystem(path)
				} else {
					r, err = OpenSQLite(path)
				}
				require.NoError(t, err)
				return r
			}
			r := open()
			refs, err := r.LoadGCReferences(ctx, "lab")
			require.NoError(t, err)
			candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: refs.Digest}
			reservation := GCReservation{ID: "gc-1", AgentID: "agent", Candidate: candidate}
			require.NoError(t, r.ReserveGC(ctx, "lab", reservation))
			require.NoError(t, r.Close())
			r = open()
			defer r.Close()
			require.NoError(t, r.ReserveGC(ctx, "lab", reservation), "lost receipt retry retains original reservation")
			manager := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := manager.Load()
			require.NoError(t, err)
			require.ErrorIs(t, manager.Save(st), ErrConflict)
			_, err = manager.Snapshot(ctx, "racing reference")
			require.ErrorIs(t, err, ErrConflict)
			require.ErrorIs(t, manager.Delete(ctx), ErrConflict)
			other := reservation
			other.ID = "gc-2"
			require.ErrorIs(t, r.ReserveGC(ctx, "lab", other), ErrConflict)
			retained, err := r.LoadGCReservation(ctx, "lab", reservation.ID)
			require.NoError(t, err)
			require.Equal(t, reservation.Candidate, retained.Candidate)
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			digest, err := candidate.Digest()
			require.NoError(t, err)
			grant := controlplane.ExecutionGrant{Operation: "gc_delete", Digest: digest, Scope: controlplane.ExecutionScope{TopologyUID: candidate.TopologyUID, ResourceUID: candidate.ResourceUID, Generation: candidate.Generation}, AgentID: "agent", SessionID: "session", CommandID: reservation.ID, ActionID: reservation.ID, Attempt: 1, Epoch: 1, TargetBootID: "boot"}
			receipt, err := controlplane.SignGCDeletionReceipt(key, grant, "boot", "receipt", candidate, time.Now())
			require.NoError(t, err)
			corrupted := receipt
			corrupted.EvidenceDigest = "changed"
			require.Error(t, r.CompleteGC(ctx, "lab", corrupted, pub))
			require.ErrorIs(t, manager.Save(st), ErrConflict)
			require.NoError(t, r.CompleteGC(ctx, "lab", receipt, pub))
			require.NoError(t, r.CompleteGC(ctx, "lab", receipt, pub))
			require.NoError(t, manager.Save(st))

		})
	}
}

func TestGCReservationRejectsStalePlanWithoutFreezingLifecycle(t *testing.T) {
	ctx := context.Background()
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	refs, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	m := state.NewManagerWithBackend(r.StateBackend("lab"))
	st, err := m.Load()
	require.NoError(t, err)
	require.NoError(t, m.Save(st))
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: refs.Digest}
	require.ErrorIs(t, r.ReserveGC(ctx, "lab", GCReservation{ID: "gc", AgentID: "agent", Candidate: c}), ErrConflict)
	st, err = m.Load()
	require.NoError(t, err)
	require.NoError(t, m.Save(st))
}

func TestGCExecutionAdmissionIsDurableSingleUse(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	c := controlplane.GCCandidate{Kind: "domain", ExternalID: "domain", OwnerUID: refs.TopologyUID, TopologyUID: refs.TopologyUID, ResourceUID: "resource", Generation: 1, InventoryDigest: refs.Digest}
	r := GCReservation{ID: "gc-once", AgentID: "host", Candidate: c}
	require.NoError(t, repo.ReserveGC(ctx, "lab", r))
	digest, _ := c.Digest()
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: 1}, AgentID: "host", CommandID: r.ID, ActionID: r.ID, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "boot"}
	require.NoError(t, repo.ClaimGCExecution(ctx, "lab", c, grant))
	after, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, refs.Digest, after.Digest, "claim metadata must not invalidate frozen inventory")
	require.Error(t, repo.ClaimGCExecution(ctx, "lab", c, grant), "lost host ledger cannot replay a consumed canonical admission")
	retained, err := repo.LoadGCReservation(ctx, "lab", r.ID)
	require.NoError(t, err)
	require.NotEmpty(t, retained.AdmissionDigest)
	require.True(t, retained.Pending())
	changed := grant
	changed.Epoch++
	require.Error(t, repo.ClaimGCExecution(ctx, "lab", c, changed))
}

func TestGCClaimPreservesRemainingBatchAdmission(t *testing.T) {
	ctx := context.Background()
	repo := resetTestRepository(t, "filesystem")
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	first := controlplane.GCCandidate{Kind: "domain", ExternalID: "first", OwnerUID: refs.TopologyUID, TopologyUID: refs.TopologyUID, ResourceUID: "first", Generation: 1, InventoryDigest: refs.Digest}
	second := first
	second.ExternalID = "second"
	second.ResourceUID = "second"
	batch, err := NewGCBatch("lab", "batch", "host", []controlplane.GCCandidate{first, second})
	require.NoError(t, err)
	require.NoError(t, repo.ReserveGCBatchWithAuthority(ctx, "lab", batch, nil))
	c := batch[0].Candidate
	digest, _ := c.Digest()
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: 1}, AgentID: "host", SessionID: "session", Attempt: 1, CommandID: batch[0].ID, ActionID: batch[0].ID, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "boot"}
	require.NoError(t, repo.ClaimGCExecution(ctx, "lab", c, grant))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	receipt, err := controlplane.SignGCDeletionReceipt(key, grant, "boot", "done", c, time.Now())
	require.NoError(t, err)
	require.NoError(t, repo.CompleteGC(ctx, "lab", receipt, pub))
	q := batch[1]
	command := controlplane.AgentCommand{ID: q.ID, ActionID: q.ID, AgentID: q.AgentID, Type: "gc_delete", TopologyUID: q.Candidate.TopologyUID, ResourceUID: q.Candidate.ResourceUID, Generation: 1, GCKind: q.Candidate.Kind, GCExternalID: q.Candidate.ExternalID, GCInventoryDigest: q.Candidate.InventoryDigest}
	require.NoError(t, repo.ValidateCommandLifecycle(ctx, "lab", command))
}
