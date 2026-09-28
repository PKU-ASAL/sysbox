package firecracker

import (
	"context"
	"fmt"
	"syscall"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Pause suspends the Firecracker VM by sending SIGSTOP to the FC process.
// The VM is frozen until Resume is called.
func (s *Substrate) Pause(ctx context.Context, h substrate.NodeHandle) error {
	return signalTrackedVM(ctx, h.ID, syscall.SIGSTOP)
}

// Resume un-freezes a VM paused with SIGSTOP.
func (s *Substrate) Resume(ctx context.Context, h substrate.NodeHandle) error {
	return signalTrackedVM(ctx, h.ID, syscall.SIGCONT)
}

func signalTrackedVM(ctx context.Context, id string, signal syscall.Signal) error {
	vm := lookupVM(id)
	if vm == nil {
		return fmt.Errorf("firecracker: VM %s not running (in-process handle not found)", id)
	}
	if err := vm.lockOperation(ctx); err != nil {
		return err
	}
	defer vm.unlockOperation()
	state := vm.snapshot()
	if !state.started || state.destroying || state.destroyed {
		return fmt.Errorf("firecracker: VM %s not running", id)
	}
	return signalVMProcess(state, signal)
}

// ReadNode is not supported for Firecracker: VMs are ephemeral processes
// that cannot be re-discovered from a new CLI invocation. Import is only
// meaningful for persistent substrates (docker, libvirt).
func (s *Substrate) ReadNode(_ context.Context, _ string) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, substrate.ErrNotSupported
}
