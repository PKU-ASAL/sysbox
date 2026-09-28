package firecracker

import (
	"context"
	"syscall"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Pause suspends the Firecracker VM by sending SIGSTOP to the FC process.
// The VM is frozen until Resume is called.
func (s *Substrate) Pause(ctx context.Context, h substrate.NodeHandle) error {
	return s.signalManifestNode(ctx, h, syscall.SIGSTOP)
}

// Resume un-freezes a VM paused with SIGSTOP.
func (s *Substrate) Resume(ctx context.Context, h substrate.NodeHandle) error {
	return s.signalManifestNode(ctx, h, syscall.SIGCONT)
}

// ReadNode does not import arbitrary external VMs. System-owned generations
// recover through DiscoverManagedNode and the versioned manifest.
func (s *Substrate) ReadNode(_ context.Context, _ string) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, substrate.ErrNotSupported
}
