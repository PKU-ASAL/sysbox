package persistence

import (
	"crypto/ed25519"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// ValidateFencedIntentDisposition authenticates the canonical stop/observation
// boundary. It is read-only; a successful check is not a durable disposition.
// Callers must hold canonical locks through committing state and dispatch.
func ValidateFencedIntentDisposition(raw []byte, d controlplane.IntentDispositionDecision, run controlplane.Run, serial int64, actions []runtime.ActionRecord, observation CommandRecord, key ed25519.PublicKey, now time.Time) error {
	ledger, err := decodeExecutionAuthority(raw)
	if err != nil {
		return err
	}
	f, ok := ledger.Fences[d.FenceID]
	if !ok || f.Receipt == nil || f.IntentDisposition != nil || f.released() || f.Reconciliation != nil || f.AdoptionDisposition != nil || f.GCDisposition != nil {
		return ErrConflict
	}
	if now.IsZero() || now.Before(ledger.ObservedAt) || now.Before(f.Receipt.ObservedAt) || d.Topology != f.Request.Topology || d.RunID != f.Request.RunID || d.JournalSHA256 != f.Request.JournalSHA256 {
		return ErrConflict
	}
	g := f.Request.Grant
	current, ok := ledger.Grants[grantScopeKey(g.Scope)]
	if !ok || !sameGrantEpoch(current, g) || current.State != controlplane.GrantRevoked || !controlplane.IsRunExecutionOperation(g.Operation) || g.Scope.ResourceUID != "" || g.Scope.Generation != 0 || run.AgentID != g.AgentID || run.Attempt <= 0 || uint64(run.Attempt) != g.Attempt {
		return ErrConflict
	}
	for _, other := range ledger.Grants {
		if (other.AgentID == g.AgentID || other.Scope.TopologyUID == g.Scope.TopologyUID) && other.State != controlplane.GrantStopped && !sameGrantEpoch(other, g) {
			return ErrConflict
		}
	}
	c := observation.Command
	r := observation.Recovery
	if !observation.Retired || c.Status != controlplane.AgentCommandStatusCompleted || c.Type != controlplane.RunRecoveryCommand || c.RecoveryMode != controlplane.RunRecoveryObserve || c.ActionID != f.Request.ID || c.AgentID != g.AgentID || c.TopologyUID != g.Scope.TopologyUID || c.ExpectedSerial != serial || c.Run == nil || !controlplane.SameRunIncarnation(*c.Run, run) || c.Run.Attempt != run.Attempt || c.Run.LeaseOwner != run.LeaseOwner || r == nil || r.CommandID != c.ID || r.TopologyUID != g.Scope.TopologyUID || validateRecoveryReport(c, r) != nil {
		return ErrConflict
	}
	digest, err := f.Request.Digest()
	if err != nil {
		return err
	}
	if r.ObservedAt.Before(f.Receipt.ObservedAt) || r.ObservedAt.After(now) || controlplane.VerifyFenceObservation(key, *r, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: f.Receipt.NewBootID}, c.LeaseOwner, c.Attempt) != nil {
		return ErrConflict
	}
	return ValidateIntentDispositionChoice(d, run, serial, actions, *r)
}
