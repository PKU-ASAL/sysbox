package agentexec

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

// Recover uses canonical API authorization, never a worker-supplied Run or
// revision. It observes identity and may commit proven journal outcomes, but
// the existing recovery engine cannot replay provider or guest effects.
func (h *HostRunExecutor) Recover(ctx context.Context, proof hostsupervisor.RunRecoveryRequest) (*controlplane.RunRecoveryReport, error) {
	if h.Bridge == nil || h.Tasks == nil {
		return nil, fmt.Errorf("host run executor is not configured")
	}
	auth, err := h.Authority.AuthorizeRunRecovery(ctx, proof)
	if err != nil {
		return nil, err
	}
	if auth.Run.AgentID != h.Tasks.AgentID || !auth.LeaseUntil.After(time.Now()) {
		return nil, fmt.Errorf("host recovery authorization expired or owner changed")
	}
	bounded, cancel := context.WithDeadline(ctx, auth.LeaseUntil)
	defer cancel()
	cmd := controlplane.AgentCommand{ID: proof.CommandID, AgentID: h.Tasks.AgentID, Type: controlplane.RunRecoveryCommand, Run: &auth.Run, RecoveryMode: auth.Mode, LeaseOwner: proof.LeaseOwner, Attempt: proof.Attempt, LeaseUntil: auth.LeaseUntil}
	report, err := NewExecutorWithBridge(h.Bridge).ObserveRecoverRun(bounded, h.Tasks.AgentID, cmd, auth)
	if err != nil {
		return nil, err
	}
	// Revalidate after slow provider reads before returning current evidence.
	current, err := h.Authority.AuthorizeRunRecovery(bounded, proof)
	if err != nil {
		return nil, err
	}
	if current.Mode != auth.Mode || !persistence.SameRunIncarnation(current.Run, auth.Run) || current.Run.Attempt != auth.Run.Attempt || current.Run.LeaseOwner != auth.Run.LeaseOwner || !current.LeaseUntil.After(time.Now()) {
		return nil, fmt.Errorf("host recovery authorization changed")
	}
	return report, nil
}
