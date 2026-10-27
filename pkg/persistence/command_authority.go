package persistence

import "context"

// HasCommandExecutionHistory includes retired grants: absence of a live grant
// alone cannot prove that a command never reached the execution boundary.
func (r *ExecutionGrantRepository) HasCommandExecutionHistory(ctx context.Context, agent, command string) (bool, error) {
	if agent == "" || !topologyName.MatchString(command) {
		return false, ErrInvalidRecord
	}
	found := false
	// Use the validated authority transaction, including stop-history integrity
	// and durable clock floor; absence is used as a no-execution proof.
	err := r.update(ctx, func(a *executionAuthority) error {
		found = a.hasCommandExecutionHistory(agent, command)
		return nil
	})
	return found, err
}

func (a *executionAuthority) hasCommandExecutionHistory(agent, command string) bool {
	for _, g := range a.Grants {
		if g.AgentID == agent && g.CommandID == command {
			return true
		}
	}
	for _, s := range a.Stops {
		if s.Grant.AgentID == agent && s.Grant.CommandID == command {
			return true
		}
	}
	return false
}
