package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/errdefs"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

var _ driver.ArtifactPreflight = (*Substrate)(nil)

// PreflightImage inspects the configured daemon's cache without pulling images.
// A cache miss cannot establish whether a registry reference is pullable.
func (s *Substrate) PreflightImage(ctx context.Context, source substrate.ArtifactSource) []substrate.PreflightCheck {
	check := substrate.PreflightCheck{Name: "docker_image", OK: false, Severity: "error"}
	if source.Kind != substrate.ArtifactOCI {
		check.Message = fmt.Sprintf("docker substrate requires artifact kind %q", substrate.ArtifactOCI)
		return []substrate.PreflightCheck{check}
	}
	reference := source.ResolvedSource
	if reference == "" {
		reference = source.Source
	}
	img, err := s.cli.ImageInspect(ctx, reference)
	switch {
	case errdefs.IsNotFound(err):
		check.OK, check.Severity = true, "warning"
		check.Message = fmt.Sprintf("Docker image %q is not cached on the target daemon; apply will attempt to pull it by reference", source.Source)
		check.Hint = "For locally built images, build or use docker load to import them into the target Docker daemon; otherwise use a pullable image reference"
	case err != nil:
		check.Message = fmt.Sprintf("cannot inspect Docker image %q: %v", source.Source, err)
		check.Hint = "check connectivity and permissions for the Docker daemon used by apply"
	default:
		digest := strings.ToLower(img.ID)
		expected := normalizeDigest(source.ExpectedDigest)
		if expected != "" && expected != digest {
			check.Message = fmt.Sprintf("docker image digest mismatch for %s: have %s, want %s", source.Source, digest, expected)
			check.Hint = "load or build the expected image on the target Docker daemon, or correct the pinned sha256 (Docker image ID)"
			break
		}
		check.OK, check.Severity = true, "info"
		check.Message = fmt.Sprintf("Docker image %q is cached on the target daemon with image ID %s", source.Source, digest)
		if expected == "" {
			check.Message += "; sha256 is not pinned"
		}
	}
	return []substrate.PreflightCheck{check}
}
