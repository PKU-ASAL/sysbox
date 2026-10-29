package hostsupervisor

import (
	"context"
	"reflect"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

func (d *Dispatcher) Control(ctx context.Context, ref TargetRef, h substrate.ExecutionHandle, g controlplane.SignedExecutionGrant, control substrate.TerminalControl) error {
	if err := control.Validate(); err != nil {
		return err
	}
	if err := controlplane.VerifyExecutionGrant(d.IssuerKey, g, h.BootID, time.Now()); err != nil {
		return err
	}
	if d.Authorize == nil {
		return guesttask.ErrUnauthorized
	}
	if err := d.Authorize(ctx, g); err != nil {
		return err
	}
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := d.load(h)
	if err != nil {
		return err
	}
	if r.Ref != ref || r.CancelRequested || r.StopReceipt != nil || !reflect.DeepEqual(r.Grant, g) {
		return guesttask.ErrConflict
	}
	target, err := d.targetRecord(ctx, r)
	if err != nil {
		return err
	}
	if target.NodeID != r.ProviderNodeID {
		return guesttask.ErrConflict
	}
	terminal, ok := target.Connection.(substrate.ConnectionTerminal)
	if !ok {
		return substrate.ErrNotSupported
	}
	h.NodeID = target.NodeID
	return terminal.ControlExecution(transport.WithTaskGrant(ctx, h.ID, g), h, control)
}
