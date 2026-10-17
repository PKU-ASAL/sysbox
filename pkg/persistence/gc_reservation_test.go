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
