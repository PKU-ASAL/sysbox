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
		for _, grant := range a.Grants {
			if grant.AgentID == agent && grant.CommandID == command {
				found = true
			}
		}
		for _, stop := range a.Stops {
			if stop.Grant.AgentID == agent && stop.Grant.CommandID == command {
				found = true
			}
		}
		return nil
	})
	return found, err
}
