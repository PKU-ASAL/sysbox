package runtime

import (
	"context"
	"fmt"
	goruntime "runtime"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/substrate"
)

func ResolvePlanArtifactDigests(ctx context.Context, topology *graph.Graph) (map[string]string, error) {
	digests := map[string]string{}
	for _, node := range topology.All() {
		switch cfg := node.Data.(type) {
		case *config.ImageConfig:
			resolvedSource, err := secret.ResolveString(ctx, executionSecretResolver, cfg.Source)
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			kind := substrate.ArtifactKind(cfg.Kind)
			if kind == substrate.ArtifactOCI {
				driverName, err := resolveSubstrateRef(cfg.Substrate)
				if err != nil {
					return nil, err
				}
				artifactDriver, err := driver.DefaultRegistry.RequireArtifact(driverName)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", node.Address, err)
				}
				handle, err := artifactDriver.ResolveImage(ctx, substrate.ArtifactSource{Kind: kind, Source: cfg.Source, ResolvedSource: resolvedSource, ExpectedDigest: cfg.SHA256, Architecture: cfg.Architecture, GuestFamily: substrate.GuestFamily(cfg.GuestFamily), Size: cfg.Size})
				if err != nil {
					return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
				}
				if err := handle.Validate(); err != nil {
					return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
				}
				digests[node.Address.String()] = handle.Identity.Digest
				continue
			}
			resolved, err := artifact.New().ResolveIdentity(artifact.IdentitySpec{Kind: kind, Source: resolvedSource, ExpectedDigest: cfg.SHA256, Architecture: cfg.Architecture, GuestFamily: substrate.GuestFamily(cfg.GuestFamily)})
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			digests[node.Address.String()] = resolved.Identity.Digest
		case *config.KernelConfig:
			resolvedSource, err := secret.ResolveString(ctx, executionSecretResolver, cfg.Source)
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			architecture := cfg.Architecture
			if architecture == "" {
				architecture = goruntime.GOARCH
			}
			resolved, err := artifact.New().ResolveIdentity(artifact.IdentitySpec{Kind: substrate.ArtifactKernel, Source: resolvedSource, ExpectedDigest: cfg.SHA256, Architecture: architecture, GuestFamily: substrate.GuestFamilyUnknown})
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			digests[node.Address.String()] = resolved.Identity.Digest
		}
	}
	return digests, nil
}
