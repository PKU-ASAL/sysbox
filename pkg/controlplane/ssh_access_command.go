package controlplane

import "fmt"

func FromSSHAccessCommand(cmd AgentCommand) (SSHAccessRequest, error) {
	if cmd.Type != "node_operation" || cmd.Operation.SSHAccess == nil || cmd.ID == "" || cmd.AgentID == "" || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 || cmd.ActionID == "" {
		return SSHAccessRequest{}, fmt.Errorf("ssh access command requires complete binding")
	}
	req := *cmd.Operation.SSHAccess
	if req.ID != cmd.ActionID {
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
