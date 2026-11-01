package persistence

import (
	"crypto/ed25519"
	"encoding/hex"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func validateIntentDispositionAuthority(f HostFenceRecord, a *executionAuthority) error {
	r := f.IntentDisposition
	if r == nil {
		return nil
	}
	d := r.Decision
	hash, err := d.Digest()
	if err != nil || a.Version != intentDispositionAuthorityVersion || f.Receipt == nil || f.GCDisposition != nil || f.AdoptionDisposition != nil || f.Reconciliation != nil || f.Release != nil || d.FenceID != f.Request.ID || d.Topology != f.Request.Topology || d.RunID != f.Request.RunID || d.JournalSHA256 != f.Request.JournalSHA256 || r.PreparedAt.Before(f.Receipt.ObservedAt) || r.PreparedAt.After(a.ObservedAt) {
		return ErrCorruptRecord
	}
	for _, hash := range []string{r.BeforeLifecycle, r.AfterLifecycle, r.BeforeCommand} {
		v, err := hex.DecodeString(hash)
		if err != nil || len(v) != 32 || hex.EncodeToString(v) != hash {
			return ErrCorruptRecord
		}
	}
	run := r.OriginalRun
	g := f.Request.Grant
	o := r.Observation
	c := o.Command
	report := o.Recovery
	if run.ID != d.RunID || run.Topology != d.Topology || !run.QueuedAt.Equal(d.RunQueuedAt) || run.Revision != d.RunRevision || run.AgentID != g.AgentID || run.Attempt <= 0 || uint64(run.Attempt) != g.Attempt || !controlplane.SameRunIncarnation(run, r.RunAfter) || r.RunAfter.Status != controlplane.RunFailed || r.RunAfter.Recoverable || !r.RunAfter.EndedAt.Equal(r.PreparedAt) || !r.RunAfter.LeaseUntil.IsZero() || r.RunAfter.HostCompletion != nil || r.RunAfter.Cleanup != nil {
		return ErrCorruptRecord
	}
	cmd := r.CommandAfter.Command
	if !r.CommandAfter.Retired || cmd.ID != g.CommandID || cmd.AgentID != g.AgentID || cmd.ActionID != run.ID || cmd.Type != "run_assigned" || cmd.Status != controlplane.AgentCommandStatusFailed || !cmd.EndedAt.Equal(r.PreparedAt) {
		return ErrCorruptRecord
	}
	if report == nil || !o.Retired || c.ID != report.CommandID || c.ActionID != d.FenceID || c.Type != controlplane.RunRecoveryCommand || c.RecoveryMode != controlplane.RunRecoveryObserve || c.Status != controlplane.AgentCommandStatusCompleted || c.AgentID != g.AgentID || c.TopologyUID != g.Scope.TopologyUID || c.ExpectedSerial != d.ExpectedSerial || c.Run == nil || !controlplane.SameRunIncarnation(*c.Run, run) || report.TopologyUID != g.Scope.TopologyUID || report.ObservedAt.Before(f.Receipt.ObservedAt) || report.ObservedAt.After(r.PreparedAt) || evidenceHash(*report) != d.ObservationSHA256 || validateRecoveryReport(c, report) != nil {
		return ErrCorruptRecord
	}
	digest, err := f.Request.Digest()
	if err != nil {
		return ErrCorruptRecord
	}
	if controlplane.VerifyFenceObservation(ed25519.PublicKey(r.HostKey), *report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: f.Receipt.NewBootID}, c.LeaseOwner, c.Attempt) != nil {
		return ErrCorruptRecord
	}
	if !r.ReleasedAt.IsZero() {
		stop, ok := a.Stops[g.Epoch]
		if !ok || !sameGrantEpoch(stop.Grant, g) || stop.Receipt != "intent-fenced:"+hash || r.ReleasedAt.Before(r.PreparedAt) || r.ReleasedAt.After(a.ObservedAt) {
			return ErrCorruptRecord
		}
	}
	return nil
}
