package agentexec

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
)

// PrepareRun is a read-only host callback; the worker cannot supply the Run
// incarnation persisted in admission evidence. Execute revalidates before work.
func (h *HostRunExecutor) PrepareRun(ctx context.Context, req hostsupervisor.RunRequest) (controlplane.Run, error) {
	if h.Bridge == nil || h.Tasks == nil {
		return controlplane.Run{}, fmt.Errorf("host run executor is not configured")
	}
	run, err := h.Bridge.ParentRun(ctx, req.RunID)
	if err != nil {
		return controlplane.Run{}, err
	}
	if run == nil || run.Topology != req.Topology || run.Revision != req.Revision || run.Op != req.Action || run.Target != req.Target || run.AgentID != h.Tasks.AgentID || run.Status != controlplane.RunRunning || run.LeaseOwner == "" || !run.LeaseUntil.After(time.Now()) {
		return controlplane.Run{}, fmt.Errorf("host run canonical claim changed")
	}
	return *run, nil
}
