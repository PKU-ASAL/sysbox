package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGCBatchAdmissionIsAtomicAndOrdered(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			ctx := context.Background()
			refs, err := r.LoadGCReferences(ctx, "lab")
			require.NoError(t, err)
			c := controlplane.GCCandidate{Kind: "container", ExternalID: "a", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "a", Generation: 1, InventoryDigest: refs.Digest}
			second := c
			second.Kind = "network"
			second.ExternalID = "b"
			second.ResourceUID = "b"
			requests, err := NewGCBatch("lab", "batch", "agent", []controlplane.GCCandidate{second, c})
			require.NoError(t, err)
			require.Equal(t, "container", requests[0].Candidate.Kind)
			bad := append([]GCReservation(nil), requests...)
			bad[1].Candidate.InventoryDigest = "changed"
			require.Error(t, r.ReserveGCBatchWithAuthority(ctx, "lab", bad, nil))
			got, err := r.ListGCReservations(ctx, "lab")
			require.NoError(t, err)
			require.Empty(t, got)
			require.NoError(t, r.ReserveGCBatchWithAuthority(ctx, "lab", requests, nil))
			require.NoError(t, r.ReserveGCBatchWithAuthority(ctx, "lab", requests, nil))
			require.Error(t, r.ReserveGCBatchWithAuthority(ctx, "lab", requests[:1], nil))
			for i, q := range requests {
				cmd := controlplane.AgentCommand{ID: q.ID, ActionID: q.ID, AgentID: "agent", Type: "gc_delete", TopologyUID: c.TopologyUID, ResourceUID: q.Candidate.ResourceUID, Generation: 1, GCKind: q.Candidate.Kind, GCExternalID: q.Candidate.ExternalID, GCInventoryDigest: refs.Digest}
				err := r.ValidateCommandLifecycle(ctx, "lab", cmd)
				if i == 0 {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrConflict)
				}
			}
		})
	}
}

func TestGCBatchRawTransitionRejectsForgedPayloadBinding(t *testing.T) {
	r := resetTestRepository(t, "filesystem")
	ctx := context.Background()
	refs, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "a", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "a", Generation: 1, InventoryDigest: refs.Digest}
	requests, err := NewGCBatch("lab", "key", "agent", []controlplane.GCCandidate{c})
	require.NoError(t, err)
	requests[0].Candidate.ExternalID = "forged"
	require.Error(t, r.ReserveGCBatchWithAuthority(ctx, "lab", requests, nil))
	got, err := r.ListGCReservations(ctx, "lab")
	require.NoError(t, err)
	require.Empty(t, got)
}
