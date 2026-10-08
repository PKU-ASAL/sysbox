package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"

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
			resolvedSource, err := secret.ResolveString(ctx, secret.EnvironmentResolver{}, cfg.Source)
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			kind := substrate.ArtifactKind(cfg.Kind)
			if kind == substrate.ArtifactOCI {
				if cfg.PullSource != "" {
					return nil, fmt.Errorf("resolve plan artifact %s: pull_source is only supported for VM artifacts", node.Address)
				}
				// Planning runs in the unprivileged control plane. Fingerprint the
				// immutable declaration here; the Agent resolves/pulls the OCI image
				// during apply and records its concrete identity in state.
				declaration := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", kind, resolvedSource, cfg.SHA256, cfg.Architecture, cfg.GuestFamily, cfg.Size)
				sum := sha256.Sum256([]byte(declaration))
				digests[node.Address.String()] = "sha256:" + hex.EncodeToString(sum[:])
				continue
			}
			pullSource, err := resolvePlanPullSource(ctx, cfg.PullSource)
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s pull_source: %w", node.Address, err)
			}
			if digest, deferred, err := planVMArtifactDigest(resolvedSource, pullSource, cfg.SHA256); err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			} else if deferred {
				identity := substrate.ArtifactIdentity{
					Kind: kind, Source: resolvedSource, Digest: digest,
					Architecture: cfg.Architecture, GuestFamily: substrate.GuestFamily(cfg.GuestFamily),
				}
				if err := identity.Validate(); err != nil {
					return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
				}
				digests[node.Address.String()] = digest
				continue
			}
			resolved, err := artifact.New().ResolveIdentityContext(ctx, artifact.IdentitySpec{Kind: kind, Source: resolvedSource, ExpectedDigest: cfg.SHA256, Architecture: cfg.Architecture, GuestFamily: substrate.GuestFamily(cfg.GuestFamily)})
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			digests[node.Address.String()] = resolved.Identity.Digest
		case *config.KernelConfig:
			resolvedSource, err := secret.ResolveString(ctx, secret.EnvironmentResolver{}, cfg.Source)
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			architecture := cfg.Architecture
			if architecture == "" {
				architecture = goruntime.GOARCH
			}
			pullSource, err := resolvePlanPullSource(ctx, cfg.PullSource)
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s pull_source: %w", node.Address, err)
			}
			if digest, deferred, err := planVMArtifactDigest(resolvedSource, pullSource, cfg.SHA256); err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			} else if deferred {
				identity := substrate.ArtifactIdentity{
					Kind: substrate.ArtifactKernel, Source: resolvedSource, Digest: digest,
					Architecture: architecture, GuestFamily: substrate.GuestFamilyUnknown,
				}
				if err := identity.Validate(); err != nil {
					return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
				}
				digests[node.Address.String()] = digest
				continue
			}
			resolved, err := artifact.New().ResolveIdentityContext(ctx, artifact.IdentitySpec{Kind: substrate.ArtifactKernel, Source: resolvedSource, ExpectedDigest: cfg.SHA256, Architecture: architecture, GuestFamily: substrate.GuestFamilyUnknown})
			if err != nil {
				return nil, fmt.Errorf("resolve plan artifact %s: %w", node.Address, err)
			}
			digests[node.Address.String()] = resolved.Identity.Digest
		}
	}
	return digests, nil
}

func resolvePlanPullSource(ctx context.Context, source string) (string, error) {
	if source == "" {
		return "", nil
	}
	return secret.ResolveString(ctx, secret.EnvironmentResolver{}, source)
}

// planVMArtifactDigest skips network I/O for pinned remote VM artifacts. A
// present local source is still hashed, so local mutation remains visible.
func planVMArtifactDigest(source, pullSource, sha string) (string, bool, error) {
	if pullSource == "" {
		if !artifact.IsURL(source) || sha == "" {
			return "", false, nil
		}
		digest, err := artifact.NormalizeSHA256(sha)
		return digest, true, err
	}
	if !artifact.IsURL(pullSource) || artifact.IsURL(source) || (strings.Contains(source, "://") && !strings.HasPrefix(source, "file://")) {
		return "", false, fmt.Errorf("pull_source requires a local source and an HTTP(S) URL")
	}
	if sha == "" {
		return "", false, fmt.Errorf("sha256 is required with pull_source")
	}
	digest, err := artifact.NormalizeSHA256(sha)
	if err != nil {
		return "", false, err
	}
	localPath := strings.TrimPrefix(source, "file://")
	if _, err := os.Stat(localPath); err == nil {
		return "", false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	return digest, true, nil
}
