package firecracker

import (
	"context"
	"fmt"
	"syscall"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Pause suspends the Firecracker VM by sending SIGSTOP to the FC process.
// The VM is frozen until Resume is called.
func (s *Substrate) Pause(_ context.Context, h substrate.NodeHandle) error {
	vmMu.Lock()
	vm, ok := vmStore[h.ID]
	vmMu.Unlock()
	if !ok || vm.cmd == nil || vm.cmd.Process == nil {
		return fmt.Errorf("firecracker: VM %s not running (in-process handle not found)", h.ID)
	}
	return vm.cmd.Process.Signal(syscall.SIGSTOP)
}

// Resume un-freezes a VM paused with SIGSTOP.
func (s *Substrate) Resume(_ context.Context, h substrate.NodeHandle) error {
	vmMu.Lock()
	vm, ok := vmStore[h.ID]
	vmMu.Unlock()
	if !ok || vm.cmd == nil || vm.cmd.Process == nil {
		return fmt.Errorf("firecracker: VM %s not running (in-process handle not found)", h.ID)
	}
	return vm.cmd.Process.Signal(syscall.SIGCONT)
}

// ReadNode is not supported for Firecracker: VMs are ephemeral processes
// that cannot be re-discovered from a new CLI invocation. Import is only
// meaningful for persistent substrates (docker, libvirt).
func (s *Substrate) ReadNode(_ context.Context, _ string) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, substrate.ErrNotSupported
}
