package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// localResetSSH selects only journal-backed, admitted SSH children. The CLI
// holds topology execution exclusion throughout admission and guest execution.
func (c *LocalSSHController) localResetSSH(ctx context.Context, scope controlplane.ExecutionScope, request *substrate.SSHAccessMutation) (*state.Resource, string, error) {
	repo, err := c.server.actionRepository(c.topology)
	if err != nil {
		return nil, "", err
	}
	st, actions, err := repo.LoadStateAndActions(ctx, c.topology)
	if err != nil {
		return nil, "", err
	}
	var found *state.Resource
	var run string
	for _, action := range actions {
		if action.Action != controlplane.PlanActionReset || action.Status != runtime.ActionUncertain || action.Phase != "verifying" || !action.EffectComplete || action.OwnerUID != scope.TopologyUID || action.ResourceUID != scope.ResourceUID || action.Generation != scope.Generation || action.Candidate == nil {
			continue
		}
		if err := action.ValidateResetSSH(st); err != nil {
			return nil, "", err
		}
		for _, dep := range action.ResetSSH {
			if !dep.Started {
				continue
			}
			if request != nil {
				if request.ID != dep.ActionID || request.Delete {
					continue
				}
				x, _ := json.Marshal(request.Config)
				y, _ := json.Marshal(dep.Config)
				if string(x) != string(y) {
					return nil, "", persistence.ErrConflict
				}
			}
			if found != nil && run != action.RunID {
				return nil, "", persistence.ErrConflict
			}
			candidate := *action.Candidate
			found, run = &candidate, action.RunID
		}
	}
	if found == nil {
		return nil, "", fmt.Errorf("local SSH node identity unavailable")
	}
	return found, run, nil
}

func (c *LocalSSHController) resolveSSH(ctx context.Context, ref hostsupervisor.TargetRef) (hostsupervisor.Target, error) {
	target, err := c.resolver.Resolve(ctx, ref)
	if err == nil {
		return target, nil
	}
	if ref.Topology != c.topology || ref.Scope.TopologyUID != c.owner {
		return target, persistence.ErrConflict
	}
	candidate, run, candidateErr := c.localResetSSH(ctx, ref.Scope, nil)
	if candidateErr != nil {
		return target, candidateErr
	}
	if candidate.Address.Name != ref.Node {
		return target, persistence.ErrConflict
	}
	return c.resolver.ResolveRunTask(ctx, ref, run)
}
