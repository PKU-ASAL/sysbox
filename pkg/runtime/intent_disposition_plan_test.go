package runtime

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func dispositionAction() ActionRecord {
	return ActionRecord{ID: "a", RunID: "run", Resource: address.Resource("sysbox_node", "node"), Action: controlplane.PlanActionCreate, Status: ActionUncertain, OwnerUID: "owner", ResourceUID: "node", Generation: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "container", OwnerUID: "owner", ResourceUID: "node", Generation: 1}}
}
func dispositionObservation(a ActionRecord) controlplane.RecoveredResource {
	return controlplane.RecoveredResource{ActionID: a.ID, Resource: a.Resource.String(), OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: a.Generation, ExternalID: a.Candidate.ExternalID, Status: "present", JournalStatus: string(a.Status), ProviderScope: "local-only"}
}
func TestIntentDispositionPlanPreservesUnknownAndDoesNotAuthorizeReplay(t *testing.T) {
	a := dispositionAction()
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{dispositionObservation(a)})
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
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{o})
	require.Contains(t, p.Blockers, "residual_absence_unproven")
	o.DeleteResidueAbsent = true
	p = PlanIntentDisposition(a, []controlplane.RecoveredResource{o})
	require.Equal(t, "confirm-absence", p.ResourceDecision)
	a.Candidate = nil
	p = PlanIntentDisposition(a, []controlplane.RecoveredResource{o})
	require.Contains(t, p.Blockers, "external_identity_missing")
	require.NotEqual(t, "confirm-absence", p.ResourceDecision)
}
func TestIntentDispositionPlanAccountsForBothResetGenerations(t *testing.T) {
	a := dispositionAction()
	a.Action = controlplane.PlanActionReset
	next := *a.Candidate
	next.Generation = 2
	next.ExternalID = "replacement"
	a.Replacement = &next
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{dispositionObservation(a)})
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
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{dispositionObservation(a)})
	require.Contains(t, p.Blockers, "guest_effect_disposition_required")
	require.False(t, p.ReplayAllowed)
}
func TestIntentDispositionPlanRejectsConflictingOrDuplicateObservation(t *testing.T) {
	for _, kind := range []string{"owner", "external", "duplicate", "journal"} {
		t.Run(kind, func(t *testing.T) {
			a := dispositionAction()
			o := dispositionObservation(a)
			observations := []controlplane.RecoveredResource{o}
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
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{o})
	require.Equal(t, "unresolved", p.ResourceDecision)
	require.Contains(t, p.Blockers, "provider_scope_not_fenced")
}
func TestIntentDispositionPlanRejectsUnboundCandidate(t *testing.T) {
	a := dispositionAction()
	a.OwnerUID = ""
	a.Candidate.OwnerUID = ""
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{dispositionObservation(a)})
	require.Equal(t, "unresolved", p.ResourceDecision)
	require.Contains(t, p.Blockers, "candidate_identity_mismatch")
}
