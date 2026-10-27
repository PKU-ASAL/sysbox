package firecracker

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// guestHostname is used both in the config drive and in the kernel ip= arg.
// Topology-scoped VM IDs can exceed the 63-byte DNS label limit; libc and
// Python fail to resolve those hostnames even when the guest boots normally.
func guestHostname(id string) string {
	name := strings.TrimPrefix(id, "sysbox-")
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("vm-%x", sum[:8])
}
