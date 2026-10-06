package api

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
)

type NodeOperationService struct {
	workspace   *WorkspaceService
	scheduler   *SchedulerService
	operations  *nodeOperationStore
	wakeCommand func(string, controlplane.AgentCommand) error
}

type ImportNodeRequest struct {
	Type      string
	Name      string
	ID        string
	Substrate string
}

func (s *NodeOperationService) enqueue(ctx context.Context, op controlplane.NodeOperation) (controlplane.NodeOperation, error) {
	if s.operations == nil || s.operations.store == nil {
		return controlplane.NodeOperation{}, fmt.Errorf("node operation store not configured")
	}
	op.CommandID = "node-op-" + op.ID
	op.LeaseEpoch = 1
	cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "node_operation", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, CreatedAt: op.CreatedAt, Operation: op, ActionID: op.ActionID, ResourceUID: op.ResourceUID, Generation: op.Generation, LeaseEpoch: op.LeaseEpoch}
	if err := s.operations.store.CreateNodeOperationDispatch(ctx, op, cmd); err != nil {
		return controlplane.NodeOperation{}, err
	}
	if s.wakeCommand != nil {
		if err := s.wakeCommand(op.AgentID, cmd); err != nil {
			slog.Warn("node operation committed; command wakeup deferred", "operation_id", op.ID, "error", err)
		}
	}
	return op, nil
}

func newNodeOperationService(workspace *WorkspaceService, scheduler *SchedulerService, operations *nodeOperationStore, wakeCommand func(string, controlplane.AgentCommand) error) *NodeOperationService {
	return &NodeOperationService{
		workspace:   workspace,
		scheduler:   scheduler,
		operations:  operations,
		wakeCommand: wakeCommand,
	}
}

func (s *NodeOperationService) CompleteFromAgent(agentID, opID string, op controlplane.NodeOperation) (controlplane.NodeOperation, error) {
	if op.ID == "" {
		op.ID = opID
	}
	if op.AgentID == "" {
		op.AgentID = agentID
	}
	if s.operations == nil || s.operations.store == nil {
		return op, fmt.Errorf("node operation store not configured")
	}
	completed, err := s.operations.store.CompleteNodeOperation(context.Background(), agentID, opID, op)
	if err != nil {
		return op, err
	}
	return *completed, nil
}

func (s *NodeOperationService) Lifecycle(ctx context.Context, topology, name, operation string, subj requestSubject) (controlplane.NodeOperation, error) {
	st, err := s.workspace.LoadState(topology)
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	res := st.FindResource(address.Resource("sysbox_node", name))
	if res == nil {
		return controlplane.NodeOperation{}, fmt.Errorf("node %q not found", name)
	}
	if _, err := driver.DefaultRegistry.RequirePower(res.Driver); err != nil {
		return controlplane.NodeOperation{}, err
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, []string{res.Driver}, "")
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	return s.enqueue(ctx, prepareNodeOperation(controlplane.NodeOperation{
		Topology:    topology,
		Workspace:   topology,
		Operation:   operation,
		Node:        name,
		Substrate:   res.Driver,
		AgentID:     agent.ID,
		RequestedBy: subj.User,
		Roles:       subj.Roles,
		ActionID:    "", ResourceUID: res.ResourceUID, Generation: res.Generation,
	}))
}

func (s *NodeOperationService) Import(ctx context.Context, topology string, req ImportNodeRequest, subj requestSubject) (controlplane.NodeOperation, error) {
	if req.Type != "sysbox_node" {
		return controlplane.NodeOperation{}, fmt.Errorf("import only supports sysbox_node, got %q", req.Type)
	}
	if err := validatePathSegment(req.Name, "name"); err != nil {
		return controlplane.NodeOperation{}, err
	}
	if req.ID == "" {
		return controlplane.NodeOperation{}, fmt.Errorf("id is required")
	}
	if req.Substrate == "" {
		return controlplane.NodeOperation{}, fmt.Errorf("substrate is required")
	}
	handler, ok := runtime.GetResourceHandler(req.Type)
	if !ok {
		return controlplane.NodeOperation{}, driver.Wrap(driver.ErrorUnsupported, req.Substrate, "ownership adoption is unavailable", nil)
	}
	if _, ok := handler.(runtime.ResourceImporter); !ok {
		return controlplane.NodeOperation{}, driver.Wrap(driver.ErrorUnsupported, req.Substrate, "ownership adoption is not implemented for this resource", nil)
	}
	if _, err := driver.DefaultRegistry.RequireImport(req.Substrate); err != nil {
		return controlplane.NodeOperation{}, err
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, []string{req.Substrate}, "")
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	return s.enqueue(ctx, prepareNodeOperation(controlplane.NodeOperation{
		Topology:    topology,
		Workspace:   topology,
		Operation:   "import",
		Type:        req.Type,
		Name:        req.Name,
		ExternalID:  req.ID,
		Substrate:   req.Substrate,
		AgentID:     agent.ID,
		RequestedBy: subj.User,
		Roles:       subj.Roles,
		ActionID:    "", ResourceUID: req.ID, Generation: 1,
	}))
}
