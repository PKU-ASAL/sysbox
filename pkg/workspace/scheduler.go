package workspace

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
)

// runAssignedCommand binds every dispatch, including outbox repair, to the
// canonical topology identity. Serial zero is the valid initial state.
func (s *Catalog) RunAssignedCommand(ctx context.Context, run *controlplane.Run) (controlplane.AgentCommand, error) {
	if s == nil || s.StateManager == nil {
		return controlplane.AgentCommand{}, fmt.Errorf("canonical topology state is unavailable")
	}
	manager, err := s.StateManager(run.Topology)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	canonical, err := manager.LoadWithContext(ctx)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	if canonical == nil || canonical.TopologyUID == "" || canonical.Meta.Serial < 0 {
		return controlplane.AgentCommand{}, fmt.Errorf("canonical topology state is unavailable")
	}
	command := ControlplaneRunAssignedCommand(run)
	command.TopologyUID = canonical.TopologyUID
	command.ActionID = run.ID
	command.ExpectedSerial = canonical.Meta.Serial
	if run.Target != "" {
		target, err := address.Parse(run.Target)
		if err != nil || !target.Equal(address.Resource(target.Type, target.Name)) || run.Op != "apply" && target.Type != "sysbox_node" {
			return controlplane.AgentCommand{}, fmt.Errorf("invalid remote Run target")
		}
		resource := canonical.FindResource(target)
		if run.Op == "apply" && (resource == nil || target.Type != "sysbox_node") {
			// A first creation has no resource identity yet. The signed request
			// still binds its exact target, topology and expected state serial.
		} else if resource == nil || resource.OwnerUID != canonical.TopologyUID || resource.ResourceUID == "" || resource.Generation < 1 {
			return controlplane.AgentCommand{}, fmt.Errorf("remote Run target has no canonical node identity: %s", run.Target)
		} else {
			command.ResourceUID, command.Generation = resource.ResourceUID, resource.Generation
		}
	}
	command.ID = controlstore.RunCommandID(*run)
	command.AgentID = run.AgentID
	command.Status = controlplane.AgentCommandStatusQueued
	command.Protocol = controlplane.AgentProtocolVersion
	command.CreatedAt = time.Now().UTC()
	return command, nil
}

func ControlplaneRunAssignedCommand(run *controlplane.Run) controlplane.AgentCommand {
	rec := RunRecord(*run)
	// Sensitive apply inputs must not enter the durable command store. The agent
	// binds var.<name> from the claim response (which re-attaches the in-memory
	// inputs), so the command's Run does not need them.
	rec.Inputs = nil
	return controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  ptrRun(rec),
	}
}

func ptrRun(run controlplane.Run) *controlplane.Run { return &run }
