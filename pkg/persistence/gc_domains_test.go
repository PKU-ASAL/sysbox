package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
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
