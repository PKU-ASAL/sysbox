package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// These tests pin the architecture invariant "Unknown observation 不等于 absent"
// (architecture.md) and the principle behind it: a provider that is temporarily
// unreachable, lacks permission, or fails to observe must not be read as
// evidence that the resource is gone. Treating unknown as absent produces
// duplicate creation and wrong deletion — the two most expensive mistakes this
// system can make.
//
// The behaviour is implemented and threaded all the way from observation to the
// apply gate, but nothing pinned the chain: any refactor that folds
// ResourceUnknown into the absent/drifted branch, or drops the
// RecoveryDecisionUnknown case, silently converts "I cannot see it" into
// "replace it".

// An unobservable node must not be classified as drift, which is the verdict
// that authorises replacement.
func TestRefreshRecoveryTreatsUnknownAsUnknownNotDrift(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:  RecoveryContextRefresh,
		HasState: true,
		Observation: substrate.NodeObservation{
			Exists: true,
			Status: substrate.NodeStatusUnknown,
			Reason: "docker daemon unreachable",
		},
	})

	require.Equal(t, model.RecoveryDecisionUnknown, plan.Decision,
		"an unobservable node must not be marked as drift; drift authorises replacement")
	require.NotEqual(t, model.RecoveryDecisionMarkDrift, plan.Decision)
	require.Equal(t, "docker daemon unreachable", plan.Reason)
}

// The unknown check must come before the "missing" conclusion. An observation
// that failed reports Exists=false as its zero value, so ordering these the
// other way round would read every failed probe as a deleted resource.
func TestRefreshRecoveryPrefersUnknownOverMissing(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:  RecoveryContextRefresh,
		HasState: true,
		Observation: substrate.NodeObservation{
			// A failed probe: nothing could be established, so Exists is false
			// only because it is the zero value.
			Exists: false,
			Status: substrate.NodeStatusUnknown,
			Reason: "observe timed out",
		},
	})

	require.Equal(t, model.RecoveryDecisionUnknown, plan.Decision,
		"a failed probe reports Exists=false as a zero value, not as evidence of absence")
}

// The mapping from observation status to plan action is the point where the
// distinction becomes consequential: absent and drifted authorise a replace,
// unknown must not.
func TestUnknownObservationDoesNotBecomeAReplaceAction(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     state.ResourceStatus
		want       model.PlanActionType
		dictates   bool
		wantReason string
	}{
		{name: "unknown refuses", status: state.ResourceUnknown, want: model.PlanActionUnknown, dictates: true},
		{name: "absent replaces", status: state.ResourceAbsent, want: model.PlanActionReplace, dictates: true},
		{name: "drifted replaces", status: state.ResourceDrifted, want: model.PlanActionReplace, dictates: true},
		// A degraded resource stays present; health surfaces the degradation
		// rather than planning a replacement.
		{name: "degraded keeps its action", status: state.ResourceDegraded, dictates: false},
		{name: "present keeps its action", status: state.ResourcePresent, dictates: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action, dictates := planActionForObservedStatus(tc.status)
			require.Equal(t, tc.dictates, dictates)
			if tc.dictates {
				require.Equal(t, tc.want, action,
					"observed status %q must map to %q", tc.status, tc.want)
				require.NotEqual(t, model.PlanActionReplace, model.PlanActionUnknown,
					"guard against the two verdicts collapsing into one constant")
			}
		})
	}
}

// Stated as its own assertion because it is the invariant, not an incidental
// consequence of the table above: unknown and absent must never produce the
// same action.
func TestUnknownAndAbsentNeverProduceTheSameAction(t *testing.T) {
	unknownAction, _ := planActionForObservedStatus(state.ResourceUnknown)
	absentAction, _ := planActionForObservedStatus(state.ResourceAbsent)

	require.NotEqual(t, absentAction, unknownAction,
		"unknown must not be actioned like absent; that is duplicate creation and wrong deletion")
}
