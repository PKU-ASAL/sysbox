package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// ValidateIntentDispositionChoice checks only the operator's evidence-bound
// resource decisions. It does not authenticate observations, prove cessation,
// settle children, mutate records or authorize release. Committers must perform
// those checks under canonical admission/authority/lifecycle exclusion.
func ValidateIntentDispositionChoice(d controlplane.IntentDispositionDecision, run controlplane.Run, serial int64, actions []runtime.ActionRecord, report controlplane.RunRecoveryReport) error {
	if _, err := d.Digest(); err != nil {
		return err
	}
	if d.RunID != run.ID || d.Topology != run.Topology || !d.RunQueuedAt.Equal(run.QueuedAt) || d.RunRevision != run.Revision || d.ExpectedSerial != serial {
		return ErrConflict
	}
	selected, journal, err := FenceJournal(run, serial, actions)
	if err != nil || journal != d.JournalSHA256 || evidenceHash(report) != d.ObservationSHA256 {
		return ErrConflict
	}
	if report.RunID != run.ID || report.AgentID != run.AgentID || report.Revision != run.Revision || !report.QueuedAt.Equal(run.QueuedAt) || report.StateSerial != serial || report.Mode != controlplane.RunRecoveryObserve || report.RunAttempt != run.Attempt || report.RunLeaseOwner != run.LeaseOwner || len(selected) != len(report.Resources) {
		return ErrConflict
	}
	choices := map[string]controlplane.IntentDispositionAction{}
	for _, choice := range d.Actions {
		choices[choice.ActionID] = choice
	}
	seen := map[string]bool{}
	byID := map[string]controlplane.RecoveredResource{}
	for _, o := range report.Resources {
		if seen[o.ActionID] {
			return ErrConflict
		}
		seen[o.ActionID] = true
		byID[o.ActionID] = o
	}
	for _, a := range selected {
		if resolved, err := runtime.IntentWithDiscoveredIdentity(a, report.Resources); err == nil {
			a = resolved
		}
		if a.OwnerUID != report.TopologyUID || !seen[a.ID] {
			return ErrConflict
		}
		o := byID[a.ID]
		if o.Resource != a.Resource.String() || o.OwnerUID != a.OwnerUID || o.ResourceUID != a.ResourceUID || o.Generation != a.Generation || o.JournalStatus != string(a.Status) || (a.Candidate != nil && o.ExternalID != a.Candidate.ExternalID) || (o.Status != "present" && o.Status != "absent" && o.Status != "unknown") {
			return ErrConflict
		}
		if runtime.ActionSettled(a) {
			if _, ok := choices[a.ID]; ok {
				return ErrConflict
			}
			continue
		}
		choice, ok := choices[a.ID]
		candidate := a.Candidate
		incomplete := choice.Decision == "retain-incomplete" || choice.Decision == "retain-replacement-incomplete"
		if choice.Decision == "retain-replacement-incomplete" {
			if !a.ResetReplacementRecorded() {
				return ErrConflict
			}
			candidate = a.Replacement
		} else if choice.Decision == "retain-incomplete" && a.Action == controlplane.PlanActionReset && !a.ResetSSHIncompleteSupported() {
			return ErrConflict
		}
		if !ok || choice.ResourceUID != a.ResourceUID || choice.Generation != a.Generation || candidate == nil || choice.ExternalID != candidate.ExternalID {
			return ErrConflict
		}
		plan := runtime.PlanIntentDisposition(a, report.Resources)
		// Stop proof is checked by the transaction's authority verifier, not by
		// resource observation. All other blockers require their own resolution.
		for _, blocker := range plan.Blockers {
			if blocker != "effect_stop_proof_required" && !(incomplete && (blocker == "guest_effect_disposition_required" || blocker == "attachment_rebuild_required")) {
				return ErrConflict
			}
		}
		if (choice.Decision == "retain" && plan.ResourceDecision != "retain-managed") || (choice.Decision == "absent" && plan.ResourceDecision != "confirm-absence") || (choice.Decision == "retain-incomplete" && plan.ResourceDecision != "retain-managed" && plan.ResourceDecision != "retain-incomplete-reset") || (choice.Decision == "retain-replacement-incomplete" && plan.ResourceDecision != "retain-incomplete-replacement") {
			return ErrConflict
		}
		delete(choices, a.ID)
	}
	if len(choices) != 0 {
		return ErrConflict
	}
	return nil
}
