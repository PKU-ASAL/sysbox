package persistence

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestStage2ProviderBatchLimitAndRegistryDeletionBoundary(t *testing.T) {
	makeCandidates := func(n int) []controlplane.GCCandidate {
		out := make([]controlplane.GCCandidate, n)
		for i := range out {
			out[i] = controlplane.GCCandidate{Kind: "container", ExternalID: fmt.Sprint(i), OwnerUID: "owner", TopologyUID: "owner", ResourceUID: fmt.Sprint(i), Generation: 1, InventoryDigest: "inventory"}
		}
		return out
	}
	_, err := NewGCBatch("lab", "key", "agent", makeCandidates(128))
	require.NoError(t, err)
	for _, n := range []int{0, 129} {
		_, err = NewGCBatch("lab", "key", "agent", makeCandidates(n))
		require.Error(t, err)
	}
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	err = r.DeleteGlobalRevision(context.Background(), "sha256:"+fmt.Sprintf("%064d", 1), time.Now())
	require.ErrorIs(t, err, ErrUnsupportedVersion)
	_, err = r.PlanCacheGC(context.Background(), t.TempDir(), "uncovered")
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}
