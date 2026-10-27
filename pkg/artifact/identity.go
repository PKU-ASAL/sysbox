package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

type IdentitySpec struct {
	Kind           substrate.ArtifactKind
	Source         string
	PullSource     string
	ExpectedDigest string
	Architecture   string
	GuestFamily    substrate.GuestFamily
	Metadata       map[string]string
}

type ResolvedIdentity struct {
	Identity  substrate.ArtifactIdentity
	Path      string
	FromCache bool
}

func (r *Resolver) ResolveIdentity(spec IdentitySpec) (ResolvedIdentity, error) {
	complete, err := r.beginPublication(Spec{Source: spec.Source, SHA256: strings.TrimPrefix(spec.ExpectedDigest, "sha256:")})
	if err != nil {
		return ResolvedIdentity{}, err
	}
	detached := *r
	detached.publish = nil
	result, err := detached.resolveIdentity(spec)
	if err != nil {
		return ResolvedIdentity{}, err
	}
	if err = complete(Result{Path: result.Path, Source: spec.Source, SHA256: strings.TrimPrefix(result.Identity.Digest, "sha256:"), FromCache: result.FromCache}); err != nil {
		return ResolvedIdentity{}, err
	}
	return result, nil
}
func (r *Resolver) resolveIdentity(spec IdentitySpec) (ResolvedIdentity, error) {
	if err := substrate.ValidateArtifactKind(spec.Kind); err != nil {
		return ResolvedIdentity{}, err
	}
	if spec.Kind == substrate.ArtifactOCI {
		return ResolvedIdentity{}, fmt.Errorf("artifact: OCI identities must be resolved by an OCI provider")
	}
	digest, err := NormalizeSHA256(spec.ExpectedDigest)
	if err != nil {
		return ResolvedIdentity{}, err
	}
	expected := strings.TrimPrefix(digest, "sha256:")
	if spec.PullSource != "" {
		if expected == "" {
			return ResolvedIdentity{}, fmt.Errorf("artifact: sha256 is required with pull_source")
		}
		if !IsURL(spec.PullSource) || IsURL(spec.Source) {
			return ResolvedIdentity{}, fmt.Errorf("artifact: pull_source requires a local source and an HTTP(S) URL")
		}
	} else if expected == "" && IsURL(spec.Source) {
		r.invalidateMutableURLCache(spec.Source)
	}
	result, err := r.Resolve(Spec{Source: spec.Source, SHA256: expected})
	if err != nil && spec.PullSource != "" && errors.Is(err, os.ErrNotExist) {
		result, err = r.Resolve(Spec{Source: spec.PullSource, SHA256: expected})
	}
	if err != nil {
		return ResolvedIdentity{}, err
	}
	identity := substrate.ArtifactIdentity{
		Kind: spec.Kind, Source: spec.Source, Digest: "sha256:" + result.SHA256,
		Architecture: spec.Architecture, GuestFamily: spec.GuestFamily,
		Metadata: cloneMetadata(spec.Metadata),
	}
	if err := identity.Validate(); err != nil {
		return ResolvedIdentity{}, err
	}
	return ResolvedIdentity{Identity: identity, Path: result.Path, FromCache: result.FromCache}, nil
}

// NormalizeSHA256 validates a VM artifact pin and returns its canonical digest.
func NormalizeSHA256(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return "", fmt.Errorf("artifact: expected SHA-256 must contain 64 hex characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("artifact: invalid expected SHA-256: %w", err)
	}
	return "sha256:" + value, nil
}

func (r *Resolver) invalidateMutableURLCache(source string) {
	sum := sha256.Sum256([]byte(source))
	basename := path.Base(urlPath(source))
	if basename == "" || basename == "/" || basename == "." {
		basename = "artifact"
	}
	_ = os.Remove(filepath.Join(r.CacheDir, hex.EncodeToString(sum[:]), basename))
}

func cloneMetadata(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// ResolveIdentityContext preserves publication admission while binding download cancellation.
func (r *Resolver) ResolveIdentityContext(ctx context.Context, spec IdentitySpec) (ResolvedIdentity, error) {
	copy := *r
	copy.ctx = ctx
	return copy.ResolveIdentity(spec)
}
