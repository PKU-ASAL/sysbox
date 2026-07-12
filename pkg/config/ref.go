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

// ResolveSubstrateRef takes "docker" or "substrate.docker.light" and returns
// the substrate type ("docker"). Returns an error on malformed input.
func ResolveSubstrateRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty substrate ref")
	}
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 1:
		return parts[0], nil
	case 3:
		return parts[1], nil
	default:
		return "", fmt.Errorf("unexpected substrate ref %q", ref)
	}
}

// ResolveName extracts the short name from a reference string.
// Accepts both bare names ("alpine") and dot-qualified references
// ("sysbox_image.alpine.id") — in both cases returns "alpine".
// Returns empty string for empty input.
func ResolveName(ref string) string {
	if ref == "" {
		return ""
	}
	if !strings.Contains(ref, ".") {
		return ref
	}
	parts := strings.Split(ref, ".")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// LooksLikeKernelRef returns true when the value looks like a
// sysbox_kernel.<name>.id reference rather than a literal filesystem
// path or URL.  Literal paths (starting with "/" or "./") and URLs
// (containing "://") are excluded so that pre-resource-era HCL keeps
// working.
func LooksLikeKernelRef(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		return false
	}
	if strings.Contains(ref, "://") {
		return false
	}
	return true
}
