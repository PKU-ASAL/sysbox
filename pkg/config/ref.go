package config

import (
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/address"
)

func ResolveResourceAddress(ref, expectedType string) (address.Address, error) {
	if ref == "" {
		return address.Address{}, fmt.Errorf("empty %s reference", expectedType)
	}
	ref = strings.TrimSuffix(ref, ".id")
	addr, err := address.Parse(ref)
	if err != nil {
		addr, err = address.Parse(expectedType + "." + ref)
	}
	if err != nil {
		return address.Address{}, fmt.Errorf("invalid %s reference %q: %w", expectedType, ref, err)
	}
	if addr.Type != expectedType {
		return address.Address{}, fmt.Errorf("reference %q has type %s; expected %s", ref, addr.Type, expectedType)
	}
	return addr, nil
}

// ResolveSubstrateRef returns the substrate type from a bare reference produced
// by traversal evaluation (e.g. substrate = substrate.docker.local evaluates to
// "docker"). Returns an error on empty or malformed input.
func ResolveSubstrateRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty substrate ref")
	}
	if strings.Contains(ref, ".") {
		return "", fmt.Errorf("unexpected substrate ref %q", ref)
	}
	return ref, nil
}

// ResolveName returns the short name embedded in a bare reference produced by
// traversal evaluation (e.g. image = sysbox_image.alpine.id evaluates to
// "alpine"). Returns empty string for empty input.
func ResolveName(ref string) string {
	return ref
}

// LooksLikeKernelRef reports whether the value is a non-empty kernel
// reference. Kernel must always be a sysbox_kernel.<name>.id reference;
// literal filesystem paths and URLs are no longer accepted.
func LooksLikeKernelRef(ref string) bool {
	return ref != ""
}
