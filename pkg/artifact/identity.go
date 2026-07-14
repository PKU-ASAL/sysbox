package artifact

import (
	"crypto/sha256"
	"encoding/hex"
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
	if err := substrate.ValidateArtifactKind(spec.Kind); err != nil {
		return ResolvedIdentity{}, err
	}
	if spec.Kind == substrate.ArtifactOCI {
		return ResolvedIdentity{}, fmt.Errorf("artifact: OCI identities must be resolved by an OCI provider")
	}
	expected := strings.TrimPrefix(spec.ExpectedDigest, "sha256:")
	if expected == "" && IsURL(spec.Source) {
		r.invalidateMutableURLCache(spec.Source)
	}
	result, err := r.Resolve(Spec{Source: spec.Source, SHA256: expected})
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
