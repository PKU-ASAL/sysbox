package runtime

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ValidateArtifactSources validates all declarations before a plan can begin
// producing side effects; provider resolution repeats the check at use time.
func ValidateArtifactSources(ctx context.Context, g *graph.Graph, workspace string, resolver secret.Resolver) error {
	for _, node := range g.All() {
		var source, digest string
		switch cfg := node.Data.(type) {
		case *config.ImageConfig:
			if substrate.ArtifactKind(cfg.Kind) == substrate.ArtifactOCI {
				continue
			}
			source, digest = cfg.Source, cfg.SHA256
		case *config.KernelConfig:
			source, digest = cfg.Source, cfg.SHA256
		default:
			continue
		}
		resolved, err := secret.ResolveString(ctx, resolver, source)
		if err == nil {
			_, err = artifact.ResolveSource(ctx, resolved, digest, workspace)
		}
		if err != nil {
			return fmt.Errorf("artifact %s: %w", node.Address, err)
		}
	}
	return nil
}
