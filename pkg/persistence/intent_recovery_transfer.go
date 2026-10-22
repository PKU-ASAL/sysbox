package persistence

import "github.com/oslab/sysbox/pkg/controlplane"

// A later independently verified fenced disposition can transfer an unresolved
// rebuild obligation to its new generation or prove the old resource absent.
// Both archives remain; strictly increasing serials prohibit reference cycles.
type IntentRecoveryTransfer struct {
	DispositionID string `json:"disposition_id"`
	ActionID      string `json:"action_id"`
}

func migrateIntentRecoveryRequirements(a *aggregate, targetID string) {
	target := a.IntentDispositions[targetID]
	for id, old := range a.IntentDispositions {
		if id == targetID {
			continue
		}
		for _, choice := range old.Decision.Actions {
			if _, done := old.Resolutions[choice.ActionID]; done {
				continue
			}
			if _, done := old.Transfers[choice.ActionID]; done {
				continue
			}
			for _, next := range target.Decision.Actions {
				if validIntentRecoveryTransfer(old, choice, target, next) {
					if old.Transfers == nil {
						old.Transfers = map[string]IntentRecoveryTransfer{}
					}
					old.Transfers[choice.ActionID] = IntentRecoveryTransfer{DispositionID: targetID, ActionID: next.ActionID}
				}
			}
		}
		a.IntentDispositions[id] = old
	}
}
func validIntentRecoveryTransfer(old IntentDispositionArchive, choice controlplane.IntentDispositionAction, target IntentDispositionArchive, next controlplane.IntentDispositionAction) bool {
	if (choice.Decision != "retain-incomplete" && choice.Decision != "retain-replacement-incomplete") || (next.Decision != "absent" && next.Decision != "retain-replacement-incomplete") || target.Decision.ExpectedSerial <= old.Decision.ExpectedSerial || old.Observation.TopologyUID == "" || old.Observation.TopologyUID != target.Observation.TopologyUID {
		return false
	}
	original, ok := old.Actions[choice.ActionID]
	if !ok {
		return false
	}
	expected := original.Candidate
	if choice.Decision == "retain-replacement-incomplete" {
		expected = original.Replacement
	}
	current, ok := target.Actions[next.ActionID]
	if !ok || expected == nil || current.Candidate == nil {
		return false
	}
	source := current.Candidate
	return current.Resource.Equal(original.Resource) && source.OwnerUID == old.Observation.TopologyUID && source.ResourceUID == choice.ResourceUID && source.Generation == choice.Generation && source.ExternalID == choice.ExternalID && source.Driver == expected.Driver && sameIntentProviderState(*source, *expected)
}
