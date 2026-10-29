package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

func dispositionAction() ActionRecord {
	return ActionRecord{ID: "a", RunID: "run", Resource: address.Resource("sysbox_node", "node"), Action: model.PlanActionCreate, Status: ActionUncertain, OwnerUID: "owner", ResourceUID: "node", Generation: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "container", OwnerUID: "owner", ResourceUID: "node", Generation: 1}}
}
func dispositionObservation(a ActionRecord) model.RecoveredResource {
	return model.RecoveredResource{ActionID: a.ID, Resource: a.Resource.String(), OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: a.Generation, ExternalID: a.Candidate.ExternalID, Status: "present", JournalStatus: string(a.Status), ProviderScope: "local-only"}
}
func TestIntentDispositionPlanPreservesUnknownAndDoesNotAuthorizeReplay(t *testing.T) {
	a := dispositionAction()
	p := PlanIntentDisposition(a, []model.RecoveredResource{dispositionObservation(a)})
	require.Equal(t, "retain-managed", p.ResourceDecision)
	require.Equal(t, ActionUncertain, p.OriginalStatus)
	require.False(t, p.ReplayAllowed)
	require.Contains(t, p.Blockers, "effect_stop_proof_required")
	require.Equal(t, ActionUncertain, a.Status)
	require.Equal(t, "container", p.Identities[0].ExternalID)
}
func TestIntentDispositionPlanRefusesGuessedAbsence(t *testing.T) {
	a := dispositionAction()
	o := dispositionObservation(a)
	o.Status = "absent"
	p := PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Contains(t, p.Blockers, "residual_absence_unproven")
	o.DeleteResidueAbsent = true
	p = PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Equal(t, "confirm-absence", p.ResourceDecision)
	a.Candidate = nil
	p = PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Contains(t, p.Blockers, "external_identity_missing")
	require.NotEqual(t, "confirm-absence", p.ResourceDecision)
}
func TestIntentDispositionPlanAccountsForBothResetGenerations(t *testing.T) {
	a := dispositionAction()
	a.Action = model.PlanActionReset
	next := *a.Candidate
	next.Generation = 2
	next.ExternalID = "replacement"
	a.Replacement = &next
	p := PlanIntentDisposition(a, []model.RecoveredResource{dispositionObservation(a)})
	require.Len(t, p.Identities, 2)
	require.Equal(t, int64(1), p.Identities[0].Generation)
	require.Equal(t, int64(2), p.Identities[1].Generation)
	require.Contains(t, p.Blockers, "reset_generation_reconciliation_required")
	require.Contains(t, p.Blockers, "replacement_observation_required")
	require.Equal(t, "review-reset", p.ResourceDecision)
}
func TestIntentDispositionPlanDoesNotConfuseResourcePresenceWithGuestCompletion(t *testing.T) {
	a := dispositionAction()
	a.ReplayForbidden = true
	p := PlanIntentDisposition(a, []model.RecoveredResource{dispositionObservation(a)})
	require.Contains(t, p.Blockers, "guest_effect_disposition_required")
	require.False(t, p.ReplayAllowed)
}
func TestIntentDispositionPlanRejectsConflictingOrDuplicateObservation(t *testing.T) {
	for _, kind := range []string{"owner", "external", "duplicate", "journal"} {
		t.Run(kind, func(t *testing.T) {
			a := dispositionAction()
			o := dispositionObservation(a)
			observations := []model.RecoveredResource{o}
			switch kind {
			case "owner":
				observations[0].OwnerUID = "foreign"
			case "external":
				observations[0].ExternalID = "replacement"
			case "duplicate":
				observations = append(observations, o)
			case "journal":
				observations[0].JournalStatus = "succeeded"
			}
			p := PlanIntentDisposition(a, observations)
			require.Equal(t, "unresolved", p.ResourceDecision)
			require.Contains(t, p.Blockers, "observation_identity_or_coverage_mismatch")
		})
	}
}

func TestIntentDispositionPlanRequiresCoveredProviderScope(t *testing.T) {
	a := dispositionAction()
	o := dispositionObservation(a)
	o.ProviderScope = "remote-daemon"
	p := PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Equal(t, "unresolved", p.ResourceDecision)
	require.Contains(t, p.Blockers, "provider_scope_not_fenced")
}
func TestIntentDispositionPlanRejectsUnboundCandidate(t *testing.T) {
	a := dispositionAction()
	a.OwnerUID = ""
	a.Candidate.OwnerUID = ""
	p := PlanIntentDisposition(a, []model.RecoveredResource{dispositionObservation(a)})
	require.Equal(t, "unresolved", p.ResourceDecision)
	require.Contains(t, p.Blockers, "candidate_identity_mismatch")
}

func TestResetDispositionBeforeReplacementPreservesSource(t *testing.T) {
	for _, phase := range []string{"accepted", "preparing", "prepared", "destroying", "destroy_returned", "destroyed"} {
		t.Run(phase, func(t *testing.T) {
			a := dispositionAction()
			a.Action = model.PlanActionReset
			a.Phase = phase
			a.Generation = 2
			a.ReplayForbidden = true
			o := dispositionObservation(a)
			o.Status = "unknown"
			old := o
			old.Generation = 1
			old.Status = "present"
			o.ResetSource = &old
			p := PlanIntentDisposition(a, []model.RecoveredResource{o})
			require.Equal(t, "retain-managed", p.ResourceDecision)
			require.Equal(t, []string{"effect_stop_proof_required"}, p.Blockers)
			require.False(t, p.ReplayAllowed)
			require.Equal(t, int64(1), p.Identities[0].Generation)
		})
	}
}
func TestResetDispositionNeverInfersUnrecordedReplacementAbsence(t *testing.T) {
	a := dispositionAction()
	a.Action = model.PlanActionReset
	a.Phase = "applying"
	a.Generation = 2
	a.ReplayForbidden = true
	o := dispositionObservation(a)
	old := o
	old.Generation = 1
	old.Status = "present"
	o.ResetSource = &old
	p := PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.NotEqual(t, "retain-managed", p.ResourceDecision)
	require.Contains(t, p.Blockers, "reset_generation_reconciliation_required")
}

func TestResetReplacementDispositionRequiresOldAbsenceAndNewExactIdentity(t *testing.T) {
	a := dispositionAction()
	a.Action = model.PlanActionReset
	a.Generation = 2
	a.Phase = "applying"
	a.ReplayForbidden = true
	replacement := *a.Candidate
	replacement.Generation = 2
	replacement.ExternalID = "new"
	a.Replacement = &replacement
	old := dispositionObservation(a)
	old.Generation = 1
	old.Status = "absent"
	old.DeleteResidueAbsent = true
	current := old
	current.Generation = 2
	current.ExternalID = "new"
	current.Status = "present"
	current.DeleteResidueAbsent = false
	o := dispositionObservation(a)
	o.Status = "unknown"
	o.ResetSource = &old
	o.ResetReplacement = &current
	p := PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Equal(t, "retain-incomplete-replacement", p.ResourceDecision)
	require.Equal(t, []string{"effect_stop_proof_required", "guest_effect_disposition_required"}, p.Blockers)
	old.Status = "present"
	p = PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.NotEqual(t, "retain-incomplete-replacement", p.ResourceDecision)
	old.Status = "absent"
	current.OwnerUID = "foreign"
	p = PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.NotEqual(t, "retain-incomplete-replacement", p.ResourceDecision)
}

func TestResetSourceDispositionPreservesUnstartedSSHDependencies(t *testing.T) {
	a := dispositionAction()
	a.Action = model.PlanActionReset
	a.Generation = 2
	a.Phase = "destroying"
	a.ReplayForbidden = true
	a.ResetSSH = []ResetSSHDependency{{Previous: state.Resource{Address: address.Resource("sysbox_ssh_access", "login"), OwnerUID: "owner", ResourceUID: "ssh", Generation: 1}}}
	old := dispositionObservation(a)
	old.Generation = 1
	o := dispositionObservation(a)
	o.Status = "unknown"
	o.ResetSource = &old
	p := PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Equal(t, "retain-managed", p.ResourceDecision)
	require.Equal(t, []string{"effect_stop_proof_required"}, p.Blockers)
	// An admitted guest mutation is not covered by the pre-replacement path.
	a.ResetSSH[0].Started = true
	require.False(t, a.ResetBeforeReplacement())
	require.NotEqual(t, "retain-managed", PlanIntentDisposition(a, []model.RecoveredResource{o}).ResourceDecision)
	// Losing the source would leave SSH metadata pointing at a deleted node.
	a.ResetSSH[0].Started = false
	old.Status = "absent"
	old.DeleteResidueAbsent = true
	require.Contains(t, PlanIntentDisposition(a, []model.RecoveredResource{o}).Blockers, "reset_ssh_source_absence_requires_disposition")
}

func TestAttachmentRetentionRequiresDurableRebuildDisposition(t *testing.T) {
	a := dispositionAction()
	a.Candidate.Attachments = []state.Attachment{{Name: "eth0", Node: a.Resource, Network: address.Resource("sysbox_network", "lan"), Driver: "docker", MAC: "02:00:00:00:00:01"}}
	o := dispositionObservation(a)
	p := PlanIntentDisposition(a, []model.RecoveredResource{o})
	require.Equal(t, "retain-managed", p.ResourceDecision)
	require.Contains(t, p.Blockers, "attachment_rebuild_required")
	o.Status = "absent"
	o.DeleteResidueAbsent = true
	require.Contains(t, PlanIntentDisposition(a, []model.RecoveredResource{o}).Blockers, "attachment_absence_proof_required")
}
