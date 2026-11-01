package controlplane

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/substrate"
)

// FromGuestSSHAccessCommand derives the exact guest payload from durable
// dispatch data. Host resolution chooses connections, never a new user or CAS
// revision that was not included in this accepted command.
func FromGuestSSHAccessCommand(cmd AgentCommand) (substrate.SSHAccessMutation, error) {
	op := cmd.Operation
	if cmd.Type != "node_operation" || cmd.ID == "" || cmd.AgentID == "" || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 || cmd.ActionID == "" ||
		!IsGuestSSHAccessOperation(op.Operation) || op.SSHMutation == nil || op.SSHAccess != nil || op.Adoption != nil || op.SSHResult != nil ||
		op.ID != cmd.ActionID || op.ActionID != cmd.ActionID || op.AgentID != cmd.AgentID || op.ResourceUID != cmd.ResourceUID || op.Generation != cmd.Generation ||
		!sshAccessID.MatchString(op.Topology) || !sshAccessID.MatchString(op.Node) || (op.CommandID != "" && op.CommandID != cmd.ID) ||
		(op.Status != NodeOperationStatusQueued && op.Status != NodeOperationStatusRunning) {
		return substrate.SSHAccessMutation{}, fmt.Errorf("guest SSH command requires complete canonical binding")
	}
	req := *op.SSHMutation
	if req.ID != op.ID || req.Delete != (op.Operation == "guest_ssh_access_delete") {
		return substrate.SSHAccessMutation{}, fmt.Errorf("guest SSH mutation does not match operation")
	}
	if err := req.Validate(); err != nil {
		return substrate.SSHAccessMutation{}, err
	}
	req.Config.AuthorizedKeys = append([]string(nil), req.Config.AuthorizedKeys...)
	return req, nil
}

func FromSSHAccessCommand(cmd AgentCommand) (SSHAccessRequest, error) {
	if cmd.Type != "node_operation" || cmd.Operation.SSHAccess == nil || cmd.ID == "" || cmd.AgentID == "" || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 || cmd.ActionID == "" {
		return SSHAccessRequest{}, fmt.Errorf("ssh access command requires complete binding")
	}
	req := *cmd.Operation.SSHAccess
	if req.ID != cmd.ActionID || req.Topology != cmd.Operation.Topology || req.Node != cmd.Operation.Node {
		return SSHAccessRequest{}, fmt.Errorf("ssh access action identity mismatch")
	}
	if err := req.Validate(); err != nil {
		return SSHAccessRequest{}, err
	}
	return req, nil
}

func FromAdoptionCommand(cmd AgentCommand) (AdoptionRequest, error) {
	if cmd.Type != "node_operation" || cmd.Operation.Adoption == nil || cmd.ID == "" || cmd.AgentID == "" || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 || cmd.ActionID == "" {
		return AdoptionRequest{}, fmt.Errorf("adoption command requires complete binding")
	}
	r := *cmd.Operation.Adoption
	if r.ID != cmd.ActionID || r.TopologyUID != cmd.TopologyUID || r.Topology != cmd.Operation.Topology || r.ResourceUID != cmd.ResourceUID || r.Generation != cmd.Generation || r.AgentID != cmd.AgentID {
		return AdoptionRequest{}, fmt.Errorf("adoption target mismatch")
	}
	if err := r.Validate(); err != nil {
		return AdoptionRequest{}, err
	}
	return r, nil
}
