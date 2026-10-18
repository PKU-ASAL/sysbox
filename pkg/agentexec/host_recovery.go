package agentexec

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
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
	if auth.FenceObservation != nil {
		if auth.Mode != controlplane.RunRecoveryObserve || auth.FenceObservation.Validate() != nil || auth.FenceObservation.HostBootID != h.Tasks.HostBootID || len(h.Tasks.HostKey) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("fence observation host boot or signing key mismatch")
		}
	}
	bounded, cancel := context.WithDeadline(ctx, auth.LeaseUntil)
	defer cancel()
	cmd := controlplane.AgentCommand{ID: proof.CommandID, AgentID: h.Tasks.AgentID, Type: controlplane.RunRecoveryCommand, Run: &auth.Run, RecoveryMode: auth.Mode, LeaseOwner: proof.LeaseOwner, Attempt: proof.Attempt, LeaseUntil: auth.LeaseUntil}
	var bridge Bridge = h.Bridge
	if auth.Mode == controlplane.RunRecoveryRecover {
		provider, ok := h.Bridge.(recoveryRepositoryProvider)
		if !ok {
			return nil, fmt.Errorf("atomic recovery authorization storage unavailable")
		}
		repo, err := provider.RecoveryActionRepository(bounded, proof, auth.Run)
		if err != nil {
			return nil, err
		}
		bridge = boundRecoveryBridge{HostRunBridge: h.Bridge, repository: repo, topology: auth.Run.Topology}
	}
	report, err := NewExecutorWithBridge(bridge).ObserveRecoverRun(bounded, h.Tasks.AgentID, cmd, auth)
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
	if (current.FenceObservation == nil) != (auth.FenceObservation == nil) || (auth.FenceObservation != nil && *current.FenceObservation != *auth.FenceObservation) {
		return nil, fmt.Errorf("host fence observation authorization changed")
	}
	if auth.FenceObservation != nil {
		signed, err := controlplane.SignFenceObservation(h.Tasks.HostKey, *report, *auth.FenceObservation, proof.LeaseOwner, proof.Attempt)
		if err != nil {
			return nil, err
		}
		return &signed, nil
	}
	return report, nil
}

type recoveryRepositoryProvider interface {
	RecoveryActionRepository(context.Context, hostsupervisor.RunRecoveryRequest, controlplane.Run) (runtime.ActionRepository, error)
}

type boundRecoveryBridge struct {
	HostRunBridge
	repository runtime.ActionRepository
	topology   string
}

func (b boundRecoveryBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	if topology != b.topology || b.repository == nil {
		return nil, fmt.Errorf("recovery topology mismatch")
	}
	return b.repository, nil
}

func (b boundRecoveryBridge) PinnedRevisionWorkspace(ctx context.Context, run controlplane.Run, revision *controlplane.GlobalRevision) (string, error) {
	provider, ok := b.HostRunBridge.(RevisionWorkspaceProvider)
	if !ok {
		return "", fmt.Errorf("immutable recovery workspace unavailable")
	}
	return provider.PinnedRevisionWorkspace(ctx, run, revision)
}
