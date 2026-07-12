package agentexec

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
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
		return e.executeImport(ctx, op)
	default:
		return fmt.Errorf("unsupported node operation %q", op.Operation)
	}
}

func (e *Executor) executePauseResume(ctx context.Context, op *controlplane.NodeOperation) error {
	mgr, err := e.bridge.StateManager(op.Topology)
	if err != nil {
		return err
	}
	st, err := mgr.Load()
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

func (e *Executor) executeImport(ctx context.Context, op *controlplane.NodeOperation) error {
	if op.Type != "sysbox_node" {
		return fmt.Errorf("import only supports sysbox_node, got %q", op.Type)
	}
	mgr, err := e.bridge.StateManager(op.Topology)
	if err != nil {
		return err
	}
	if err := mgr.CheckMutationSafety(); err != nil {
		return err
	}
	st, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	addr := address.Resource(op.Type, op.Name)
	if r := st.FindResource(addr); r != nil {
		return fmt.Errorf("resource %s.%s already in state", op.Type, op.Name)
	}
	handler, ok := runtime.GetResourceHandler(op.Type)
	if !ok {
		return fmt.Errorf("resource handler %q not registered", op.Type)
	}
	importer, ok := handler.(runtime.ResourceImporter)
	if !ok {
		return fmt.Errorf("resource %s does not support import", op.Type)
	}
	resource, err := importer.Import(ctx, addr, op.Substrate, op.ExternalID)
	if err != nil {
		return err
	}
	st.AddResource(resource)
	owner := fmt.Sprintf("sysbox-agent:import:%s:%s.%s", op.Topology, op.Type, op.Name)
	return mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: owner})
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
