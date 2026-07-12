package substrate

import "github.com/oslab/sysbox/pkg/address"

// StateReader is the narrow read-only state view available to node drivers.
type StateReader interface {
	ResourceInstance(address.Address) map[string]any
}
