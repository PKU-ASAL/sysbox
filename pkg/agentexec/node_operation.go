package agentexec

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (e *Executor) ExecuteNodeOperation(ctx context.Context, op controlplane.NodeOperation) controlplane.NodeOperation {
	op.Status = controlplane.NodeOperationStatusRunning
	op.StartedAt = time.Now().UTC()
	op.Audit = append(op.Audit, nodeOperationEvent(op, "start", controlplane.NodeOperationStatusRunning, "node operation started"))
	err := e.executeNodeOperation(ctx, &op)
	op.EndedAt = time.Now().UTC()
	if err != nil {
		op.Status = controlplane.NodeOperationStatusFailed
		op.Err = err.Error()
		op.Audit = append(op.Audit, nodeOperationEvent(op, "complete", controlplane.NodeOperationStatusFailed, err.Error()))
	} else {
		op.Status = controlplane.NodeOperationStatusDone
		op.Err = ""
		op.Audit = append(op.Audit, nodeOperationEvent(op, "complete", controlplane.NodeOperationStatusDone, "node operation completed"))
	}
	return op
}

func (e *Executor) executeNodeOperation(ctx context.Context, op *controlplane.NodeOperation) error {
	if e == nil || e.bridge == nil {
		return fmt.Errorf("executor bridge is not configured")
	}
	switch op.Operation {
	case "pause", "resume":
		return e.executePauseResume(ctx, op)
	case "import":
		return fmt.Errorf("managed ownership adoption: %w", substrate.ErrNotSupported)
	default:
		return fmt.Errorf("unsupported node operation %q", op.Operation)
	}
}

func (e *Executor) executePauseResume(ctx context.Context, op *controlplane.NodeOperation) error {
	unlock, err := e.bridge.LockTopology(ctx, op.Topology)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	mgr, err := e.bridge.StateManager(op.Topology)
	if err != nil {
		return err
	}
	st, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return err
	}
	res := st.FindResource(address.Resource("sysbox_node", op.Node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", op.Node))
	}
	if res == nil {
		return fmt.Errorf("node %q not found", op.Node)
	}
	identity := substrate.OperationIdentity{ActionID: op.ActionID, OwnerUID: res.OwnerUID, TopologyUID: st.TopologyUID, ResourceUID: op.ResourceUID, Generation: op.Generation}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("node operation requires complete target proof: %w", err)
	}
	if res.OwnerUID != st.TopologyUID || res.ResourceUID != op.ResourceUID || res.Generation != op.Generation {
		return fmt.Errorf("%w: node operation target ownership or generation changed", runtime.ErrPlanStale)
	}
	ctx = substrate.WithOperationIdentity(ctx, identity)
	powerDriver, err := driver.DefaultRegistry.RequirePower(res.Driver)
	if err != nil {
		return err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		return err
	}
	handle, err := res.ReconstructHandle(stateDriver)
	if err != nil {
		return err
	}
	if op.Operation == "pause" {
		return powerDriver.Pause(ctx, handle)
	}
	return powerDriver.Resume(ctx, handle)
}

func nodeOperationEvent(op controlplane.NodeOperation, action, status, message string) controlplane.Event {
	return controlplane.Event{
		ProjectID: op.ProjectID,
		Workspace: op.Workspace,
		Resource:  op.Resource(),
		Action:    action,
		Status:    status,
		Actor:     op.RequestedBy,
		Roles:     append([]string{}, op.Roles...),
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
}
