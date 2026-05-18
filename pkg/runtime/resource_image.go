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

	res := artifact.New()

	// Resolve disk image sources through the artifact cache (URL or local path).
	rootfs, qcow2 := cfg.Rootfs, cfg.QCow2
	var resolvedSHA string
	for _, entry := range []struct {
		src   string
		label string
		dst   *string
	}{
		{cfg.Rootfs, "rootfs", &rootfs},
		{cfg.QCow2, "qcow2", &qcow2},
	} {
		if entry.src == "" {
			continue
		}
		r, err := res.Resolve(artifact.Spec{Source: entry.src, SHA256: cfg.SHA256})
		if err != nil {
			return fmt.Errorf("image %s %s: %w", n.ID.Name, entry.label, err)
		}
		if r.FromCache {
			e.logf("[apply] image %s: %s cache hit (%s)\n", n.ID.Name, entry.label, r.Path)
		} else if artifact.IsURL(entry.src) {
			e.logf("[apply] image %s: %s fetched to %s\n", n.ID.Name, entry.label, r.Path)
		}
		*entry.dst = r.Path
		resolvedSHA = r.SHA256
	}

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{
		DockerRef: cfg.DockerRef,
		Rootfs:    rootfs,
		QCow2:     qcow2,
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
			"source":     cfg.Rootfs + cfg.QCow2,
			"sha256":     resolvedSHA,
		},
	})
	return nil
}
