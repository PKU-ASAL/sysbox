package firecracker

import "github.com/oslab/sysbox/pkg/substrate"

// Verify interface compliance.
var _ substrate.Substrate = (*Substrate)(nil)

// RegisterWithKernel is a convenience constructor that also registers
// the substrate in the global registry.
func RegisterWithKernel(kernelPath, rootfsDir string) {
	s := New(kernelPath, rootfsDir)
	substrate.Register(s)
}
