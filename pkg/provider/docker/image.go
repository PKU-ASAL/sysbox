package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"

	"github.com/oslab/sysbox/pkg/substrate"
)

// PrepareImage ensures the docker image is available locally.
// Inspects first; if missing, pulls. This avoids unnecessary network
// round-trips and lets the tool work offline when the image is cached.
func (s *Substrate) PrepareImage(ctx context.Context, spec substrate.ImageSpec) (substrate.ImageRef, error) {
	if spec.DockerRef == "" {
		return substrate.ImageRef{}, fmt.Errorf("docker substrate requires ImageSpec.DockerRef")
	}

	if img, _, err := s.cli.ImageInspectWithRaw(ctx, spec.DockerRef); err == nil {
		return substrate.ImageRef{ID: img.ID, Repository: spec.DockerRef}, nil
	}

	rc, err := s.cli.ImagePull(ctx, spec.DockerRef, image.PullOptions{})
	if err != nil {
		return substrate.ImageRef{}, fmt.Errorf("docker pull %s: %w", spec.DockerRef, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return substrate.ImageRef{}, fmt.Errorf("drain image pull: %w", err)
	}

	img, _, err := s.cli.ImageInspectWithRaw(ctx, spec.DockerRef)
	if err != nil {
		return substrate.ImageRef{}, fmt.Errorf("inspect image after pull: %w", err)
	}

	return substrate.ImageRef{
		ID:         img.ID,
		Repository: spec.DockerRef,
	}, nil
}
