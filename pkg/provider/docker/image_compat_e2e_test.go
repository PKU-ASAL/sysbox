//go:build e2e

package docker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// Run only against an explicitly selected disposable daemon.
func TestDockerDigestCompatibilityE2E(t *testing.T) {
	if os.Getenv("SYSBOX_DOCKER_COMPAT_E2E") != "1" {
		t.Skip("requires disposable compatibility daemon")
	}
	require.NotEmpty(t, os.Getenv("DOCKER_HOST"))
	sub, err := New()
	require.NoError(t, err)
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reference := os.Getenv("SYSBOX_DOCKER_COMPAT_IMAGE")
	if reference == "" {
		reference = "alpine:3.22"
	}
	source := substrate.ArtifactSource{Kind: substrate.ArtifactOCI, Source: reference, Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	initial, err := sub.ResolveImage(ctx, source)
	require.NoError(t, err)
	inspected, _, err := sub.cli.ImageInspectWithRaw(ctx, initial.ID)
	require.NoError(t, err)
	require.NotEmpty(t, inspected.RepoDigests)
	_, digest, ok := strings.Cut(inspected.RepoDigests[0], "@")
	require.True(t, ok)
	source.Source = inspected.RepoDigests[0]
	source.ExpectedDigest = digest
	locked, err := sub.ResolveImage(ctx, source)
	require.NoError(t, err)
	require.Equal(t, digest, locked.Identity.Digest)
	require.Equal(t, inspected.ID, locked.ID)
	source.ExpectedDigest = "sha256:" + strings.Repeat("0", 64)
	_, err = sub.ResolveImage(ctx, source)
	require.Error(t, err)
	t.Logf("verified manifest=%s local-image=%s", digest, locked.ID)
}
