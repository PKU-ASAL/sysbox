package api

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
)

type NodeOperationService struct {
	workspace      *WorkspaceService
	scheduler      *SchedulerService
	operations     *nodeOperationStore
	publishCommand func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)
}

type ImportNodeRequest struct {
	Type      string
	Name      string
	ID        string
	Substrate string
}

func newNodeOperationService(workspace *WorkspaceService, scheduler *SchedulerService, operations *nodeOperationStore, publishCommand func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)) *NodeOperationService {
	return &NodeOperationService{
		workspace:      workspace,
		scheduler:      scheduler,
		operations:     operations,
		publishCommand: publishCommand,
	}
}

func (s *NodeOperationService) CompleteFromAgent(agentID, opID string, op controlplane.NodeOperation) (controlplane.NodeOperation, error) {
	if op.ID == "" {
		op.ID = opID
	}
	if op.ID != opID {
		return op, fmt.Errorf("operation id mismatch")
	}
	if op.AgentID == "" {
		op.AgentID = agentID
	}
	if op.AgentID != agentID {
		return op, fmt.Errorf("operation agent id mismatch")
	}
	if op.EndedAt.IsZero() && (op.Status == controlplane.NodeOperationStatusDone || op.Status == controlplane.NodeOperationStatusFailed) {
		op.EndedAt = time.Now().UTC()
	}
	s.operations.Save(op)
	return op, nil
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
	agent, err := s.scheduler.SelectAgent(ctx, []string{res.Driver}, "")
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	op := s.operations.Create(controlplane.NodeOperation{
		Topology:    topology,
		Workspace:   topology,
		Operation:   operation,
		Node:        name,
		Substrate:   res.Driver,
		AgentID:     agent.ID,
		RequestedBy: subj.User,
		Roles:       subj.Roles,
	})
	if _, err := s.publishCommand(ctx, agent.ID, controlplane.AgentCommand{
		Type:      "node_operation",
		Operation: op,
	}); err != nil {
		return controlplane.NodeOperation{}, err
	}
	return op, nil
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
	if _, err := driver.DefaultRegistry.RequireImport(req.Substrate); err != nil {
		return controlplane.NodeOperation{}, err
	}
	agent, err := s.scheduler.SelectAgent(ctx, []string{req.Substrate}, "")
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	op := s.operations.Create(controlplane.NodeOperation{
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
	})
	if _, err := s.publishCommand(ctx, agent.ID, controlplane.AgentCommand{
		Type:      "node_operation",
		Operation: op,
	}); err != nil {
		return controlplane.NodeOperation{}, err
	}
	return op, nil
}
