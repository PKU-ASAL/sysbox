package runtime

import (
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/oslab/sysbox/pkg/address"
)

func runtimeExternalName(topology, kind string, resource address.Address) string {
	safeName := externalResourceComponent(resource)
	if topology == "" {
		switch kind {
		case "actor":
			return "sysbox-actor-" + safeName
		case "net":
			return safeName
		default:
			return "sysbox-" + safeName
		}
	}
	return "sysbox-lab-" + sanitizeExternalName(topology) + "-" + kind + "-" + safeName
}

func networkExternalName(topology string, resource address.Address) string {
	name := externalResourceComponent(resource)
	if topology == "" {
		return name
	}
	return "lab-" + sanitizeExternalName(topology) + "-net-" + name
}

// Plain, already-normalized names retain the existing provider name. Indexed
// and module addresses include a digest of the full canonical address, so keys
// that normalize alike never collapse into the same provider request.
func externalResourceComponent(resource address.Address) string {
	name := sanitizeExternalName(resource.Name)
	if !resource.Key.IsSet() && len(resource.ModulePath) == 0 && name == resource.Name {
		return name
	}
	digest := sha256.Sum256([]byte(resource.String()))
	if len(name) > 32 {
		name = name[:32]
	}
	return fmt.Sprintf("%s-%x", name, digest[:12])
}

func sanitizeExternalName(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "default"
	}
	return out
}

func shortLinuxName(prefix, name string) string {
	base := name
	max := 15
	full := prefix + "-" + base
	if len(full) <= max {
		return full
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(base))
	sum := fmt.Sprintf("%08x", h.Sum32())
	headLen := max - len(prefix) - 1 - len(sum) - 1
	if headLen < 1 {
		return (prefix + "-" + sum)[:max]
	}
	return prefix + "-" + base[:headLen] + "-" + sum
}
