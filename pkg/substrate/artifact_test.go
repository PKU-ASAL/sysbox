package substrate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactIdentityValidateAndClone(t *testing.T) {
	identity := ArtifactIdentity{
		Kind:         ArtifactQCow2,
		Source:       "/images/ubuntu.qcow2",
		Digest:       "sha256:5fa5b05e5ec239858c4531485d6023b0896448c2df7c63b34f8dae6ea6051a44",
		Architecture: "amd64",
		GuestFamily:  GuestFamilyLinux,
		Metadata:     map[string]string{"release": "noble"},
	}
	require.NoError(t, identity.Validate())
	clone := identity.Clone()
	clone.Metadata["release"] = "changed"
	require.Equal(t, "noble", identity.Metadata["release"])

	require.ErrorContains(t, (ArtifactIdentity{Kind: "vmdk"}).Validate(), "artifact kind")
	require.ErrorContains(t, (ArtifactIdentity{Kind: ArtifactQCow2, Source: "x", Digest: "bad", Architecture: "amd64", GuestFamily: GuestFamilyLinux}).Validate(), "digest")
}

func TestArtifactHandleRequiresImmutableIdentityAndProviderID(t *testing.T) {
	handle := ArtifactHandle{
		Identity: ArtifactIdentity{Kind: ArtifactOCI, Source: "alpine:latest", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "amd64", GuestFamily: GuestFamilyLinux},
		ID:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	require.NoError(t, handle.Validate())
	handle.ID = ""
	require.ErrorContains(t, handle.Validate(), "provider artifact ID")
}
