package guesttask

import (
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

// FromExecutionCommand is the canonical payload both the issuer and Agent use;
// the worker cannot select a different task ID, input, or output quota.
func FromExecutionCommand(cmd controlplane.AgentCommand) (Request, error) {
	if cmd.Type != "guest_execution" || cmd.Execution == nil || cmd.ActionID == "" {
		return Request{}, fmt.Errorf("command is not a bound guest execution")
	}
	r := Request{UserID: 65534, ID: cmd.ActionID, Args: cmd.ExecutionRequest.Argv, Environment: cmd.ExecutionRequest.Environment, WorkingDir: cmd.ExecutionRequest.WorkingDirectory, OutputLimit: substrate.DefaultExecOutputLimit}
	return r, r.Validate()
}
