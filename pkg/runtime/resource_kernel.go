package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// createKernel resolves a sysbox_kernel resource into a local on-disk path
// via the artifact resolver (downloading + caching as needed) and records it
// in state. Other resources (sysbox_node) reference the resolved path by
// looking up state["sysbox_kernel", name].path.
func (e *Executor) createKernel(_ context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.KernelConfig)
	if !ok {
		return fmt.Errorf("kernel %s: wrong data type", n.ID)
	}
	if cfg.Source == "" {
		return fmt.Errorf("kernel %s: source required", n.ID.Name)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}

	res, err := artifact.New().Resolve(artifact.Spec{Source: cfg.Source, SHA256: cfg.SHA256})
	if err != nil {
		return fmt.Errorf("kernel %s: %w", n.ID.Name, err)
	}
	if res.FromCache {
		fmt.Printf("[apply] kernel %s: cache hit (%s)\n", n.ID.Name, res.Path)
	} else if artifact.IsURL(cfg.Source) {
		fmt.Printf("[apply] kernel %s: fetched to %s\n", n.ID.Name, res.Path)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_kernel",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"path":             res.Path,
			"source":           cfg.Source,
			"sha256":           res.SHA256,
			"cmdline_template": cfg.CmdlineTemplate,
		},
	})
	return nil
}
