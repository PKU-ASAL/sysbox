package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (e *Executor) createImage(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.ImageConfig)
	if !ok {
		return fmt.Errorf("image %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}

	// Resolve rootfs source through the artifact resolver. This makes
	// URL-based rootfs identical to local-path rootfs from the substrate's
	// perspective: the substrate always sees an absolute local path.
	rootfs := cfg.Rootfs
	var rootfsSHA string
	if rootfs != "" {
		res, err := artifact.New().Resolve(artifact.Spec{Source: rootfs, SHA256: cfg.SHA256})
		if err != nil {
			return fmt.Errorf("image %s rootfs: %w", n.ID.Name, err)
		}
		if res.FromCache {
			e.logf("[apply] image %s: rootfs cache hit (%s)\n", n.ID.Name, res.Path)
		} else if artifact.IsURL(cfg.Rootfs) {
			e.logf("[apply] image %s: rootfs fetched to %s\n", n.ID.Name, res.Path)
		}
		rootfs = res.Path
		rootfsSHA = res.SHA256
	}

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{
		DockerRef: cfg.DockerRef,
		Rootfs:    rootfs,
		Size:      cfg.Size,
	})
	if err != nil {
		return err
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_image",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"image_id":   ref.ID,
			"repository": ref.Repository,
			"source":     cfg.Rootfs,
			"sha256":     rootfsSHA,
		},
	})
	return nil
}
