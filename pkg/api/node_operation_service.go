package api

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
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

type AdoptionPlan struct {
	Decision    string `json:"decision"`
	Topology    string `json:"topology"`
	TopologyUID string `json:"topology_uid"`
	Serial      int64  `json:"serial"`
	Address     string `json:"address"`
	ExternalID  string `json:"external_id"`
	Substrate   string `json:"substrate"`
	AgentID     string `json:"agent_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// AdoptionPlan is read-only. It never probes or mutates a provider; the
// confirm phase requires an owning-agent evidence record with this exact
// topology identity and serial.
func (s *NodeOperationService) AdoptionPlan(ctx context.Context, topology string, req ImportNodeRequest) (AdoptionPlan, error) {
	plan := AdoptionPlan{Decision: "unsupported", Topology: topology, Address: req.Type + "." + req.Name, ExternalID: req.ID, Substrate: req.Substrate}
	if req.Type != "sysbox_node" || req.Name == "" || req.ID == "" || req.Substrate == "" {
		return plan, fmt.Errorf("adoption plan requires type, name, external id and substrate")
	}
	st, err := s.workspace.LoadState(topology)
	if err != nil {
		return plan, err
	}
	plan.TopologyUID, plan.Serial = st.TopologyUID, st.Meta.Serial
	if st.FindResource(address.Resource(req.Type, req.Name)) != nil {
		return plan, fmt.Errorf("%w: adoption address already exists", persistence.ErrConflict)
	}
	if _, err := driver.DefaultRegistry.RequireOwnershipAdoption(req.Substrate); err != nil {
		plan.Reason = err.Error()
		return plan, nil
	}
	if s.scheduler == nil {
		plan.Reason = "owning agent scheduler unavailable"
		return plan, nil
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, []string{req.Substrate}, "")
	if err != nil {
		plan.Reason = err.Error()
		return plan, nil
	}
	plan.Decision = "inspection_required"
	plan.AgentID = agent.ID
	plan.Reason = "owning agent inspection evidence required before confirm"
	return plan, nil
}

func (s *NodeOperationService) enqueue(ctx context.Context, topologyUID string, op controlplane.NodeOperation) (controlplane.NodeOperation, error) {
	if topologyUID == "" || op.ResourceUID == "" || op.Generation < 1 {
		return controlplane.NodeOperation{}, fmt.Errorf("node operation requires complete topology and resource ownership proof")
	}
	if s.operations == nil || s.operations.store == nil {
		return controlplane.NodeOperation{}, fmt.Errorf("node operation store not configured")
	}
	op.CommandID = "node-op-" + op.ID
	op.LeaseEpoch = 1
	cmd := controlplane.AgentCommand{TopologyUID: topologyUID, ID: op.CommandID, AgentID: op.AgentID, Type: "node_operation", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, CreatedAt: op.CreatedAt, Operation: op, ActionID: op.ActionID, ResourceUID: op.ResourceUID, Generation: op.Generation, LeaseEpoch: op.LeaseEpoch}
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
	if s.workspace.lockAdmission != nil {
		unlock := s.workspace.lockAdmission(topology)
		defer unlock()
	}
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
	if res.OwnerUID != st.TopologyUID {
		return controlplane.NodeOperation{}, fmt.Errorf("node ownership differs from topology")
	}
	return s.enqueue(ctx, st.TopologyUID, prepareNodeOperation(controlplane.NodeOperation{
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

func (s *NodeOperationService) SSHAccess(ctx context.Context, topology string, node string, req controlplane.SSHAccessRequest, operation string, subj requestSubject) (controlplane.NodeOperation, error) {
	if operation != "ssh_access_apply" && operation != "ssh_access_delete" {
		return controlplane.NodeOperation{}, fmt.Errorf("unsupported ssh access operation")
	}
	if req.Topology != topology || req.Node != node {
		return controlplane.NodeOperation{}, fmt.Errorf("ssh access target mismatch")
	}
	if err := req.Validate(); err != nil {
		return controlplane.NodeOperation{}, err
	}
	st, err := s.workspace.LoadState(topology)
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	res := st.FindResource(address.Resource("sysbox_node", node))
	if res == nil {
		return controlplane.NodeOperation{}, fmt.Errorf("node %q not found", node)
	}
	if res.OwnerUID != st.TopologyUID {
		return controlplane.NodeOperation{}, fmt.Errorf("node ownership differs from topology")
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, []string{res.Driver}, "")
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	op := prepareNodeOperation(controlplane.NodeOperation{ID: req.ID, Topology: topology, Workspace: topology, Operation: operation, Node: node, Substrate: res.Driver, AgentID: agent.ID, RequestedBy: subj.User, Roles: subj.Roles, ResourceUID: res.ResourceUID, Generation: res.Generation, SSHAccess: &req})
	return s.enqueue(ctx, st.TopologyUID, op)
}

func (s *NodeOperationService) AdoptionConfirm(ctx context.Context, req controlplane.AdoptionRequest, subj requestSubject) (controlplane.NodeOperation, error) {
	if err := req.Validate(); err != nil {
		return controlplane.NodeOperation{}, err
	}
	if req.AgentID == "" {
		req.AgentID = subj.User
	}
	st, err := s.workspace.LoadState(req.Topology)
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	if st.TopologyUID != req.TopologyUID || st.Meta.Serial != req.ExpectedSerial {
		return controlplane.NodeOperation{}, fmt.Errorf("%w: adoption plan is stale", persistence.ErrConflict)
	}
	addr, err := address.Parse(req.Address)
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	if st.FindResource(addr) != nil {
		return controlplane.NodeOperation{}, fmt.Errorf("%w: adoption address already exists", persistence.ErrConflict)
	}
	if _, err := driver.DefaultRegistry.RequireOwnershipAdoption(req.Provider); err != nil {
		return controlplane.NodeOperation{}, err
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, req.Topology, []string{req.Provider}, req.AgentID)
	if err != nil {
		return controlplane.NodeOperation{}, err
	}
	if agent.ID != req.AgentID {
		return controlplane.NodeOperation{}, fmt.Errorf("%w: owning agent changed", persistence.ErrConflict)
	}
	op := prepareNodeOperation(controlplane.NodeOperation{ID: req.ID, Topology: req.Topology, Workspace: req.Topology, Operation: "adoption_apply", Node: addr.Name, Type: addr.Type, Name: addr.Name, ExternalID: req.ExternalID, Substrate: req.Provider, AgentID: req.AgentID, RequestedBy: subj.User, Roles: subj.Roles, ResourceUID: req.ResourceUID, Generation: req.Generation, Adoption: &req})
	return s.enqueue(ctx, st.TopologyUID, op)
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
	return controlplane.NodeOperation{}, driver.Wrap(driver.ErrorUnsupported, req.Substrate, "ownership adoption plan/confirm is not implemented", nil)
}
