package runtime

import (
	"fmt"
	"hash/fnv"
	"strings"
)

func runtimeExternalName(topology, kind, name string) string {
	safeName := sanitizeExternalName(name)
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

func networkExternalName(topology, name string) string {
	if topology == "" {
		return sanitizeExternalName(name)
	}
	return "lab-" + sanitizeExternalName(topology) + "-net-" + sanitizeExternalName(name)
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
	base := sanitizeExternalName(name)
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
