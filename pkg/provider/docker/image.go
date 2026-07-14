package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"

	"github.com/oslab/sysbox/pkg/substrate"
)

// ResolveImage ensures the Docker image is available and returns its immutable identity.
// Inspects first; if missing, pulls. This avoids unnecessary network
// round-trips and lets the tool work offline when the image is cached.
func (s *Substrate) ResolveImage(ctx context.Context, source substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	if source.Kind != substrate.ArtifactOCI {
		return substrate.ArtifactHandle{}, fmt.Errorf("docker substrate requires artifact kind %q", substrate.ArtifactOCI)
	}

	reference := source.ResolvedSource
	if reference == "" {
		reference = source.Source
	}
	img, _, err := s.cli.ImageInspectWithRaw(ctx, reference)
	if err != nil {
		rc, pullErr := s.cli.ImagePull(ctx, reference, image.PullOptions{})
		if pullErr != nil {
			return substrate.ArtifactHandle{}, fmt.Errorf("docker pull %s: %w", source.Source, pullErr)
		}
		defer rc.Close()
		if _, pullErr := io.Copy(io.Discard, rc); pullErr != nil {
			return substrate.ArtifactHandle{}, fmt.Errorf("drain image pull: %w", pullErr)
		}
		img, _, err = s.cli.ImageInspectWithRaw(ctx, reference)
		if err != nil {
			return substrate.ArtifactHandle{}, fmt.Errorf("inspect image after pull: %w", err)
		}
	}
	digest := strings.ToLower(img.ID)
	if expected := normalizeDigest(source.ExpectedDigest); expected != "" && expected != digest {
		return substrate.ArtifactHandle{}, fmt.Errorf("docker image digest mismatch for %s: have %s, want %s", source.Source, digest, expected)
	}
	handle := substrate.ArtifactHandle{Identity: substrate.ArtifactIdentity{
		Kind: source.Kind, Source: source.Source, Digest: digest, Architecture: source.Architecture,
		GuestFamily: source.GuestFamily, Metadata: source.Metadata,
	}, ID: img.ID}
	if err := handle.Validate(); err != nil {
		return substrate.ArtifactHandle{}, err
	}
	return handle, nil
}

func normalizeDigest(digest string) string {
	digest = strings.ToLower(digest)
	if digest != "" && !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}
	return digest
}
