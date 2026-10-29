package persistence

import (
	"context"
	"fmt"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestGCReferenceDomainsDeclareCompleteOrNotApplicable(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	ctx := context.Background()
	require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
		a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), Driver: "docker", OwnerUID: a.State.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: "container", Attributes: state.MustAttributes(map[string]any{"revision": "sha256:rev", "artifact_digest": "sha256:artifact", "cache_key": "cache-key", "placement_agent": "agent"})}}
		return nil
	}))
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	for _, domain := range []string{"revision", "artifact", "cache", "placement"} {
		require.Equal(t, "complete", refs.Domains[domain].Status, domain)
		require.NotEmpty(t, refs.Domains[domain].Digest, domain)
	}
	refs, err = repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, "complete", refs.Domains["revision"].Status)
}
func TestGCReferenceDomainsRejectMalformedDomainAttributes(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	ctx := context.Background()
	require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
		a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), Driver: "docker", OwnerUID: a.State.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: "container", Attributes: state.MustAttributes(map[string]any{"artifact_digest": ""})}}
		return nil
	}))
	_, err = repo.LoadGCReferences(ctx, "lab")
	require.ErrorIs(t, err, ErrCorruptRecord)
}

func TestGCReferencesRequireExplicitExternalInventoryProducer(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	ctx := context.Background()
	require.NoError(t, repo.SetExternalGCInventoryProvider(func(context.Context, string) (map[string][]string, error) {
		return map[string][]string{"revision": {"sha256:remote"}, "artifact": {"sha256:artifact"}, "cache": {"cache-key"}, "placement": {"agent-remote"}}, nil
	}))
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	for _, domain := range []string{"revision", "artifact", "cache", "placement"} {
		require.Equal(t, "complete", refs.Domains[domain].Status, domain)
		require.Equal(t, 1, refs.Domains[domain].Count, domain)
	}
}

func TestGCReferencesFailClosedWhenExternalInventoryUnavailable(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	require.NoError(t, repo.SetExternalGCInventoryProvider(func(context.Context, string) (map[string][]string, error) {
		return nil, ErrConflict
	}))
	_, err = repo.LoadGCReferences(context.Background(), "lab")
	require.ErrorIs(t, err, ErrConflict)
}

func TestGCReferenceDomainsIncludeRetainedSnapshots(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			repo, err := OpenFilesystem(t.TempDir())
			require.NoError(t, err)
			defer repo.Close()
			ctx := context.Background()
			value := "retained"
			if malformed {
				value = ""
			}
			require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
				a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), Driver: "docker", OwnerUID: a.State.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: "container", Attributes: state.MustAttributes(map[string]any{"artifact_digest": value})}}
				return nil
			}))
			_, err = repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "retain")
			require.NoError(t, err)
			require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error { a.State.Resources = nil; return nil }))
			refs, err := repo.LoadGCReferences(ctx, "lab")
			if malformed {
				require.ErrorIs(t, err, ErrCorruptRecord)
				require.Equal(t, GCReferenceSnapshot{}, refs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "complete", refs.Domains["artifact"].Status)
			require.Equal(t, 1, refs.Domains["artifact"].Count)
		})
	}
}

func TestGCExternalDomainsRejectIncompleteEnumeration(t *testing.T) {
	for _, external := range []map[string][]string{nil, {}, {"revision": {"one"}}} {
		refs := GCReferenceSnapshot{Domains: map[string]GCReferenceDomain{}}
		require.Error(t, mergeExternalDomains(&refs, external))
	}
}

func TestGCExternalDomainsPreserveLocalEvidence(t *testing.T) {
	external := map[string][]string{"revision": {}, "artifact": {"remote"}, "cache": {}, "placement": {}}
	refs := GCReferenceSnapshot{Domains: map[string]GCReferenceDomain{"artifact": {Status: "complete", Digest: "local-a", Count: 2}}}
	require.NoError(t, mergeExternalDomains(&refs, external))
	require.Equal(t, 3, refs.Domains["artifact"].Count)
	other := GCReferenceSnapshot{Domains: map[string]GCReferenceDomain{"artifact": {Status: "complete", Digest: "local-b", Count: 2}}}
	require.NoError(t, mergeExternalDomains(&other, external))
	require.NotEqual(t, refs.Domains["artifact"].Digest, other.Domains["artifact"].Digest)
}

func TestGCExternalInventoryCannotAuthorizeLocalDeletion(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	ctx := context.Background()
	require.NoError(t, repo.SetExternalGCInventoryProvider(func(context.Context, string) (map[string][]string, error) {
		return map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, nil
	}))
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
	require.ErrorIs(t, repo.ReserveGC(ctx, "lab", GCReservation{ID: "delete", AgentID: "agent", Candidate: candidate}), ErrConflict)
	_, err = repo.LoadGCReservation(ctx, "lab", "delete")
	require.ErrorIs(t, err, ErrNotFound)
}
