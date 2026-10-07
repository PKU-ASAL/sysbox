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
