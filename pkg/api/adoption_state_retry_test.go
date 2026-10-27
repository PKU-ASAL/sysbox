package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAdoptionStateRetryPreservesCachedRepository(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.stateBackend = "sqlite://" + filepath.Join(t.TempDir(), "lifecycle.db")
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	op := controlplane.NodeOperation{ID: "adopt", Adoption: &controlplane.AdoptionRequest{
		ID: "adopt", AgentID: "agent", Topology: "lab", TopologyUID: st.TopologyUID,
		Address: "sysbox_node.web", Provider: "docker", ExternalID: "external", ResourceUID: "node", Generation: 1, ExpectedSerial: st.Meta.Serial,
		Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "fingerprint"},
	}}
	evidence := json.RawMessage(`{"container_name":"web"}`)
	require.NoError(t, s.commitAdoptionState(ctx, op, evidence))
	require.NoError(t, s.commitAdoptionState(ctx, op, evidence))
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, st.Meta.Serial+1, after.Meta.Serial)
	require.Len(t, after.Resources, 1)
}

func TestLateAdoptionCompletionCannotBypassGCFrozenReferences(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		refs, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		candidate := controlplane.GCCandidate{Kind: "domain", ExternalID: "external", TopologyUID: st.TopologyUID, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, InventoryDigest: refs.Digest}
		require.NoError(t, repo.ReserveGC(ctx, "lab", persistence.GCReservation{ID: "gc", AgentID: "host", Candidate: candidate}))
		op := controlplane.NodeOperation{ID: "adopt", Topology: "lab", Adoption: &controlplane.AdoptionRequest{ID: "adopt", Topology: "lab", TopologyUID: st.TopologyUID, Address: "sysbox_node.web", Provider: "libvirt", AgentID: "host", ExternalID: "external", ResourceUID: "node", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: "external", Fingerprint: "before"}}}
		require.ErrorIs(t, s.commitAdoptionState(ctx, op, json.RawMessage(`{"domain_uuid":"external"}`)), persistence.ErrConflict)
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.Empty(t, after.Resources)
		require.Equal(t, st.Meta.Serial, after.Meta.Serial)
	})
}
