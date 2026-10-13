package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
)

// RecoverReturnedDestroy is invoked only while the host coordinator holds the
// original executor lock. It authorizes no effects and cannot complete cleanup.
func (h *HostRunExecutor) RecoverReturnedDestroy(ctx context.Context, req hostsupervisor.RunRequest, original controlplane.Run) (json.RawMessage, error) {
	if h.Bridge == nil || h.Tasks == nil || req.Action != "destroy" || original.ID != req.RunID || original.AgentID != h.Tasks.AgentID {
		return nil, fmt.Errorf("trusted Destroy recovery unavailable")
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
	if current == nil || !persistence.SameRunIncarnation(original, *current) || current.Attempt != original.Attempt || current.LeaseOwner != original.LeaseOwner || current.HostCompletion != nil || (current.Status != controlplane.RunRunning && !(current.Status == controlplane.RunFailed && current.Recoverable && current.Err == "run lease expired")) {
		return nil, fmt.Errorf("Destroy incarnation changed")
	}
	provider, ok := h.Bridge.(ActionRepositoryProvider)
	if !ok {
		return nil, fmt.Errorf("Destroy recovery journal unavailable")
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
		return nil, fmt.Errorf("Destroy topology owner changed")
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
	failed.Err = "controller stopped after durable deletion return; observe resources before continuing cleanup"
	return hostsupervisor.DestroyFailureEvidence(&failed, actions)
}
