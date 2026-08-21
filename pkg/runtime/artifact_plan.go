package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	goruntime "runtime"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
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
				// Planning runs in the unprivileged control plane. Fingerprint the
				// immutable declaration here; the Agent resolves/pulls the OCI image
				// during apply and records its concrete identity in state.
				declaration := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", kind, resolvedSource, cfg.SHA256, cfg.Architecture, cfg.GuestFamily, cfg.Size)
				sum := sha256.Sum256([]byte(declaration))
				digests[node.Address.String()] = "sha256:" + hex.EncodeToString(sum[:])
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
