package hostexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
)

// RecoverInterruptedRun is invoked only while the host coordinator holds the
// original executor lock. It authorizes no effects and cannot complete cleanup.
func (h *RunExecutor) RecoverInterruptedRun(ctx context.Context, req hostsupervisor.RunRequest, original controlplane.Run) (json.RawMessage, error) {
	if h.Bridge == nil || h.Tasks == nil || (req.Action != "destroy" && req.Action != "reset") || original.Op != req.Action || (original.ParentID != "" && original.Op != "reset") || original.ID != req.RunID || original.AgentID != h.Tasks.AgentID {
		return nil, fmt.Errorf("trusted interrupted lifecycle recovery unavailable")
	}
	unlock, err := h.Bridge.LockTopology(ctx, req.Topology)
	if err != nil {
		return nil, err
	}
	defer unlock()
	current, err := h.Bridge.ParentRun(ctx, original.ID)
	if err != nil {
		return nil, err
	}
	if current == nil || !controlplane.SameRunIncarnation(original, *current) || current.Attempt != original.Attempt || current.LeaseOwner != original.LeaseOwner || current.HostCompletion != nil || (current.Status != controlplane.RunRunning && !(current.Status == controlplane.RunFailed && current.Recoverable && current.Err == "run lease expired")) {
		return nil, fmt.Errorf("interrupted Run incarnation changed")
	}
	provider, ok := h.Bridge.(ActionRepositoryProvider)
	if !ok {
		return nil, fmt.Errorf("interrupted Run journal unavailable")
	}
	repo, err := provider.ActionRepository(req.Topology)
	if err != nil {
		return nil, err
	}
	st, err := repo.LoadState(ctx, req.Topology)
	if err != nil {
		return nil, err
	}
	if st.TopologyUID != req.TopologyUID {
		return nil, fmt.Errorf("interrupted Run topology owner changed")
	}
	actions, err := repo.LoadActions(ctx, req.Topology)
	if err != nil {
		return nil, err
	}
	if err := h.Tasks.ProveRunStopped(ctx, original.ID); err != nil {
		return nil, err
	}
	failed := original
	failed.Status, failed.EndedAt = controlplane.RunFailed, time.Now().UTC()
	if original.Op == "reset" {
		origin := &original
		if original.ParentID != "" {
			origin, err = ResolveResetOrigin(ctx, h.Bridge, &original)
			if err != nil {
				return nil, err
			}
		}
		failed.Err = "controller stopped at a durable reset boundary; explicit continuation required"
		return hostsupervisor.ResetFailureEvidence(&failed, origin, actions)
	}
	failed.Err = "controller stopped after durable deletion return; observe resources before continuing cleanup"
	return hostsupervisor.DestroyFailureEvidence(&failed, actions)
}
