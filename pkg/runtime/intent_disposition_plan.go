package runtime

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

// IntentDispositionIdentity is inventory, not an instruction to create or delete
// the external resource. Reset may retain two different generation identities.
type IntentDispositionIdentity struct {
	Role        string `json:"role"`
	Provider    string `json:"provider"`
	ExternalID  string `json:"external_id"`
	OwnerUID    string `json:"owner_uid"`
	ResourceUID string `json:"resource_uid"`
	Generation  int64  `json:"generation"`
}

// IntentDispositionPlan is deliberately non-authorizing. An authenticated
// observation is only one input to a canonical disposition; the caller must
// separately prove effect cessation and serialize all admission/child checks.
type IntentDispositionPlan struct {
	ActionID         string                      `json:"action_id"`
	OriginalStatus   ActionStatus                `json:"original_status"`
	ResourceDecision string                      `json:"resource_decision"`
	ReplayAllowed    bool                        `json:"replay_allowed"`
	Identities       []IntentDispositionIdentity `json:"identities"`
	Blockers         []string                    `json:"blockers"`
}

func PlanIntentDisposition(a ActionRecord, observations []controlplane.RecoveredResource) IntentDispositionPlan {
	p := IntentDispositionPlan{ActionID: a.ID, OriginalStatus: a.Status, ResourceDecision: "unresolved", Identities: []IntentDispositionIdentity{}, Blockers: []string{"effect_stop_proof_required"}}
	if a.Candidate != nil {
		p.Identities = append(p.Identities, dispositionIdentity("candidate", a.Candidate))
	}
	if a.Replacement != nil {
		p.Identities = append(p.Identities, dispositionIdentity("replacement", a.Replacement))
	}
	if a.DispositionID != "" {
		p.ResourceDecision = "already-disposed"
		p.Blockers = []string{}
		return p
	}
	if a.Status == ActionSucceeded {
		p.ResourceDecision = "already-completed"
		p.Blockers = []string{}
		return p
	}
	if a.Action == controlplane.PlanActionReset && a.Candidate != nil && a.Candidate.Generation != a.Generation {
		p.ResourceDecision = "review-reset"
		if !a.ResetBeforeReplacement() {
			p.Blockers = append(p.Blockers, "reset_generation_reconciliation_required")
			return p
		}
		var original *controlplane.RecoveredResource
		for i := range observations {
			o := &observations[i]
			if o.ActionID != a.ID {
				continue
			}
			if original != nil || o.Resource != a.Resource.String() || o.OwnerUID != a.OwnerUID || o.ResourceUID != a.ResourceUID || o.Generation != a.Generation || o.ExternalID != a.Candidate.ExternalID || o.JournalStatus != string(a.Status) || o.ResetReplacement != nil {
				p.Blockers = append(p.Blockers, "observation_identity_or_coverage_mismatch")
				return p
			}
			original = o
		}
		if original == nil || original.ResetSource == nil {
			p.Blockers = append(p.Blockers, "reset_source_observation_required")
			return p
		}
		source := a
		source.Action = controlplane.PlanActionRead
		source.Generation = a.Candidate.Generation
		source.ReplayForbidden = false
		old := original.ResetSource
		if old.ResetSource != nil || old.ResetReplacement != nil {
			p.Blockers = append(p.Blockers, "observation_identity_or_coverage_mismatch")
			return p
		}
		result := PlanIntentDisposition(source, []controlplane.RecoveredResource{*old})
		result.Identities = p.Identities
		return result
	}
	if a.ReplayForbidden && !a.EffectComplete {
		p.Blockers = append(p.Blockers, "guest_effect_disposition_required")
	}
	if len(a.ResetSSH) > 0 {
		p.Blockers = append(p.Blockers, "reset_ssh_disposition_required")
	}
	if a.Candidate == nil || a.Candidate.ExternalID == "" {
		p.Blockers = append(p.Blockers, "external_identity_missing")
		return p
	}
	c := a.Candidate
	if a.OwnerUID == "" || a.ResourceUID == "" || a.Generation <= 0 || c.Driver == "" || c.OwnerUID != a.OwnerUID || c.ResourceUID != a.ResourceUID || c.Generation != a.Generation || !c.Address.Equal(a.Resource) {
		p.Blockers = append(p.Blockers, "candidate_identity_mismatch")
		return p
	}
	if len(c.Attachments) > 0 {
		p.Blockers = append(p.Blockers, "attachment_disposition_required")
	}
	var observed *controlplane.RecoveredResource
	for i := range observations {
		if observations[i].ActionID != a.ID {
			continue
		}
		if observed != nil {
			p.Blockers = append(p.Blockers, "observation_identity_or_coverage_mismatch")
			return p
		}
		observed = &observations[i]
	}
	if observed == nil {
		p.Blockers = append(p.Blockers, "owning_agent_observation_required")
	} else {
		o := observed
		if o.Resource != a.Resource.String() || o.OwnerUID != a.OwnerUID || o.ResourceUID != a.ResourceUID || o.Generation != a.Generation || o.ExternalID != c.ExternalID || o.JournalStatus != string(a.Status) {
			p.Blockers = append(p.Blockers, "observation_identity_or_coverage_mismatch")
			return p
		}
		if o.ProviderScope != "local-only" {
			p.Blockers = append(p.Blockers, "provider_scope_not_fenced")
			return p
		}
		switch o.Status {
		case "present":
			p.ResourceDecision = "retain-managed"
		case "absent":
			if o.ProviderScope != "local-only" || !o.DeleteResidueAbsent {
				p.Blockers = append(p.Blockers, "residual_absence_unproven")
			} else {
				p.ResourceDecision = "confirm-absence"
			}
		default:
			p.Blockers = append(p.Blockers, "resource_presence_unknown")
		}
	}
	if a.Action == controlplane.PlanActionReset {
		p.ResourceDecision = "review-reset"
		p.Blockers = append(p.Blockers, "reset_generation_reconciliation_required")
		if a.Replacement != nil {
			p.Blockers = append(p.Blockers, "replacement_observation_required")
		}
	}
	return p
}

func dispositionIdentity(role string, r *state.Resource) IntentDispositionIdentity {
	return IntentDispositionIdentity{Role: role, Provider: r.Driver, ExternalID: r.ExternalID, OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation}
}

// ResetBeforeReplacement identifies a durably recorded phase before any new
// generation creation was admitted. It never proves cessation by itself.
func (a ActionRecord) ResetBeforeReplacement() bool {
	if a.Action != controlplane.PlanActionReset || a.Candidate == nil || a.Replacement != nil || a.EffectComplete || len(a.ResetSSH) > 0 || a.Generation <= 1 || a.Candidate.Generation != a.Generation-1 {
		return false
	}
	switch a.Phase {
	case "accepted", "preparing", "prepared", "destroying", "destroy_returned", "destroyed":
		return true
	}
	return false
}
