package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"

	"github.com/oslab/sysbox/pkg/artifact"
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
	complete, err := artifact.BeginPublication(ctx, artifact.Spec{Source: reference, SHA256: source.ExpectedDigest})
	if err != nil {
		return substrate.ArtifactHandle{}, err
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
	candidates := imageDigestCandidates(img.ID, img.RepoDigests)
	if expected := normalizeDigest(source.ExpectedDigest); expected != "" {
		if !digestMatches(candidates, expected) {
			return substrate.ArtifactHandle{}, fmt.Errorf("docker image digest mismatch for %s: have %s, want %s", source.Source, strings.Join(candidates, ", "), expected)
		}
		// Record the locked identity, not the daemon-local one: Docker 20.10
		// reports the config digest as ID while Docker 29 reports the manifest
		// digest, and state must compare equal across daemon versions.
		digest = expected
	}
	handle := substrate.ArtifactHandle{Identity: substrate.ArtifactIdentity{
		Kind: source.Kind, Source: source.Source, Digest: digest, Architecture: source.Architecture,
		GuestFamily: source.GuestFamily, Metadata: source.Metadata,
	}, ID: img.ID}
	if err := handle.Validate(); err != nil {
		return substrate.ArtifactHandle{}, err
	}
	if err := complete(artifact.Result{Path: "oci:" + img.ID, Source: reference, SHA256: strings.TrimPrefix(digest, "sha256:")}); err != nil {
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

// imageDigestCandidates returns every immutable identity a daemon may mean by
// "this image": the ID it reports (config digest on Docker 20.10, manifest
// digest on Docker 29) plus every repo digest (manifest references).
func imageDigestCandidates(id string, repoDigests []string) []string {
	candidates := make([]string, 0, 1+len(repoDigests))
	if id = strings.ToLower(id); id != "" {
		candidates = append(candidates, id)
	}
	for _, ref := range repoDigests {
		at := strings.LastIndex(ref, "@")
		if at < 0 {
			continue
		}
		candidates = append(candidates, normalizeDigest(ref[at+1:]))
	}
	return candidates
}

func digestMatches(candidates []string, expected string) bool {
	for _, candidate := range candidates {
		if candidate == expected {
			return true
		}
	}
	return false
}
