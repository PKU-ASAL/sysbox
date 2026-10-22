package persistence

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"math"
	"strings"
	"time"
)

// intentDispositionPostimage builds a candidate lifecycle postimage after the
// caller authenticates and prepares the exact fenced decision. It is not an
// independently callable repository mutation and does not release authority.
func intentDispositionPostimage(a *aggregate, d controlplane.IntentDispositionDecision, at time.Time, observations ...controlplane.RecoveredResource) (map[string]runtime.ActionRecord, error) {
	if a == nil || a.State == nil || d.ID == "" || d.ExpectedSerial != a.Serial || a.Serial == math.MaxInt64 || at.IsZero() || len(d.Actions) == 0 {
		return nil, ErrConflict
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var next aggregate
	if err = json.Unmarshal(raw, &next); err != nil {
		return nil, err
	}
	archive := map[string]runtime.ActionRecord{}
	addresses := map[string]bool{}
	for _, choice := range d.Actions {
		action, ok := next.Actions[choice.ActionID]
		original := action
		if ok && action.Action == controlplane.PlanActionReset && action.Phase == "verifying" && len(action.ResetSSH) > 0 {
			if resolved, err := runtime.ResetSSHWithObservedResults(action, observations); err == nil {
				action = resolved
			}
		}
		if ok && action.ResetSSHResultsRecorded() && choice.Decision == "retain" {
			if choice.ResourceUID != action.ResourceUID || choice.Generation != action.Generation || choice.ExternalID != action.Candidate.ExternalID {
				return nil, ErrConflict
			}
			if err := publishResetSSHDisposition(&next, action, at, addresses); err != nil {
				return nil, err
			}
			archive[action.ID] = original
			action = original
			action.DispositionID = "intent:" + d.ID
			next.Actions[action.ID] = action
			continue
		}
		incomplete := choice.Decision == "retain-incomplete" || choice.Decision == "retain-replacement-incomplete"
		replacement := choice.Decision == "retain-replacement-incomplete" && action.ResetReplacementRecorded()
		if !ok || runtime.ActionSettled(action) || action.OwnerUID != next.State.TopologyUID || action.Candidate == nil || (action.Replacement != nil && !replacement) || (action.Action == controlplane.PlanActionReset && !action.ResetBeforeReplacement() && !replacement) || (len(action.ResetSSH) > 0 && (!action.ResetBeforeReplacement() || choice.Decision != "retain")) || (action.ReplayForbidden && !action.EffectComplete && !action.ResetBeforeReplacement() && !incomplete) || addresses[action.Resource.String()] {
			return nil, ErrConflict
		}
		if len(action.ResetSSH) > 0 {
			if action.ValidateResetSSH(next.State) != nil {
				return nil, ErrConflict
			}
		}
		candidate := action.Candidate
		if replacement {
			candidate = action.Replacement
		}
		if choice.ResourceUID != action.ResourceUID || choice.Generation != action.Generation || choice.ExternalID != candidate.ExternalID || candidate.ExternalID == "" || (len(candidate.Attachments) > 0 && !incomplete) {
			return nil, ErrConflict
		}
		current := next.State.FindResource(action.Resource)
		expectedCurrent := candidate
		if replacement && current != nil && current.Generation == action.Candidate.Generation {
			expectedCurrent = action.Candidate
		}
		if current != nil && (current.OwnerUID != expectedCurrent.OwnerUID || current.ResourceUID != expectedCurrent.ResourceUID || current.Generation != expectedCurrent.Generation || current.Driver != expectedCurrent.Driver || current.ExternalID != expectedCurrent.ExternalID || (len(current.Attachments) > 0 && (!incomplete || evidenceHash(current.Attachments) != evidenceHash(expectedCurrent.Attachments)))) {
			return nil, ErrConflict
		}
		for _, resource := range next.State.Resources {
			if !resource.Address.Equal(action.Resource) && (resource.ResourceUID == candidate.ResourceUID || (resource.Driver == candidate.Driver && resource.ExternalID == candidate.ExternalID)) {
				return nil, ErrConflict
			}
		}
		switch choice.Decision {
		case "retain", "retain-incomplete", "retain-replacement-incomplete":
			retained := *candidate
			if current != nil && !replacement {
				retained = *current
			}
			var private map[string]json.RawMessage
			if json.Unmarshal(retained.Private, &private) != nil || len(private) == 0 {
				return nil, ErrConflict
			}
			if action.ResetBeforeReplacement() || incomplete {
				if err := retained.DeleteRuntimeValue("reset_handle"); err != nil {
					return nil, err
				}
			}
			if incomplete {
				if err := retained.RequireRecovery(d.ID, retained.ResourceUID, retained.Generation); err != nil {
					return nil, err
				}
			}
			retained.Status = state.ResourceUnknown
			if retained.CreatedAt.IsZero() {
				retained.CreatedAt = at
			}
			next.State.RemoveResource(action.Resource)
			next.State.AddResource(retained)
			next.State.FindResource(action.Resource).UpdatedAt = at
		case "absent":
			next.State.RemoveResource(action.Resource)
		default:
			return nil, ErrConflict
		}
		addresses[action.Resource.String()] = true
		archive[action.ID] = action
		action.DispositionID = "intent:" + d.ID
		next.Actions[action.ID] = action
	}
	next.Serial++
	next.UpdatedAt = at
	*a = next
	return archive, nil
}

type IntentDispositionArchive struct {
	Transfers   map[string]IntentRecoveryTransfer      `json:"transfers,omitempty"`
	Resolutions map[string]runtime.ActionRecord        `json:"resolutions,omitempty"`
	Decision    controlplane.IntentDispositionDecision `json:"decision"`
	Run         controlplane.Run                       `json:"run"`
	Actions     map[string]runtime.ActionRecord        `json:"actions"`
	Request     controlplane.HostFenceRequest          `json:"request"`
	Fence       controlplane.HostFenceReceipt          `json:"fence"`
	Observation controlplane.RunRecoveryReport         `json:"observation"`
	HostKey     []byte                                 `json:"host_key"`
	CommittedAt time.Time                              `json:"committed_at"`
}

func PrepareIntentDispositionLifecycle(authorityRaw, lifecycleRaw []byte, d controlplane.IntentDispositionDecision, run controlplane.Run, observation CommandRecord, key []byte, now time.Time) ([]byte, error) {
	return prepareIntentDispositionLifecycle(authorityRaw, lifecycleRaw, d, run, observation, key, now, now)
}

func prepareIntentDispositionLifecycle(authorityRaw, lifecycleRaw []byte, d controlplane.IntentDispositionDecision, run controlplane.Run, observation CommandRecord, key []byte, now, decidedAt time.Time) ([]byte, error) {
	a, err := decodeAggregate(lifecycleRaw)
	if err != nil {
		return nil, err
	}
	if err = validateAggregate(a, d.Topology); err != nil {
		return nil, err
	}
	if _, exists := a.IntentDispositions[d.ID]; exists {
		return nil, ErrConflict
	}
	if a.State.TopologyUID != observation.Command.TopologyUID || gcPending(a) || ownershipPending(a) || len(a.ResetContinuations) > 0 {
		return nil, ErrConflict
	}
	if err = ValidateFencedIntentDisposition(authorityRaw, d, run, a.Serial, actionValues(a), observation, key, now); err != nil {
		return nil, err
	}
	auth, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	f := auth.Fences[d.FenceID]
	original := actionValues(a)
	if _, err = intentDispositionPostimage(a, d, decidedAt, observation.Recovery.Resources...); err != nil {
		return nil, err
	}
	archive := map[string]runtime.ActionRecord{}
	for _, action := range original {
		if action.RunID == run.ID {
			archive[action.ID] = action
		}
	}
	if a.IntentDispositions == nil {
		a.IntentDispositions = map[string]IntentDispositionArchive{}
	}
	a.IntentDispositions[d.ID] = IntentDispositionArchive{Decision: d, Run: DurableRun(run), Actions: archive, Request: f.Request, Fence: *f.Receipt, Observation: *observation.Recovery, HostKey: append([]byte(nil), key...), CommittedAt: decidedAt}
	migrateIntentRecoveryRequirements(a, d.ID)
	a.Format = IntentDispositionFormatVersion
	if err = validateAggregate(a, d.Topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

const IntentDispositionFormatVersion = 15

func validateIntentDispositionArchives(a *aggregate) error {
	if len(a.IntentDispositions) > 0 && a.Format != IntentDispositionFormatVersion {
		return ErrCorruptRecord
	}
	for id, record := range a.IntentDispositions {
		d := record.Decision
		if id != d.ID || d.FenceID != record.Request.ID || d.JournalSHA256 != record.Request.JournalSHA256 || d.RunID != record.Request.RunID || d.Topology != record.Request.Topology || record.CommittedAt.IsZero() || record.CommittedAt.Before(record.Observation.ObservedAt) || record.Observation.ObservedAt.Before(record.Fence.ObservedAt) || record.Observation.TopologyUID != record.Request.Grant.Scope.TopologyUID || record.Run.AgentID != record.Request.Grant.AgentID || uint64(record.Run.Attempt) != record.Request.Grant.Attempt || controlplane.VerifyHostFenceReceipt(record.Request, record.Fence) != nil {
			return ErrCorruptRecord
		}
		digest, err := record.Request.Digest()
		if err != nil {
			return ErrCorruptRecord
		}
		proof := record.Observation.HostObservation
		if proof == nil || controlplane.VerifyFenceObservation(ed25519.PublicKey(record.HostKey), record.Observation, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: record.Fence.NewBootID}, proof.DeliveryOwner, proof.DeliveryAttempt) != nil {
			return ErrCorruptRecord
		}
		actions := make([]runtime.ActionRecord, 0, len(record.Actions))
		for actionID, old := range record.Actions {
			if actionID != old.ID || validateAction(old) != nil || old.Revision < 1 || old.RunID != record.Run.ID {
				return ErrCorruptRecord
			}
			actions = append(actions, old)
		}
		if ValidateIntentDispositionChoice(d, record.Run, d.ExpectedSerial, actions, record.Observation) != nil {
			return ErrCorruptRecord
		}
		for actionID, transfer := range record.Transfers {
			if _, also := record.Resolutions[actionID]; also {
				return ErrCorruptRecord
			}
			target, ok := a.IntentDispositions[transfer.DispositionID]
			if !ok {
				return ErrCorruptRecord
			}
			matched := false
			for _, choice := range d.Actions {
				for _, next := range target.Decision.Actions {
					if choice.ActionID == actionID && next.ActionID == transfer.ActionID && validIntentRecoveryTransfer(record, choice, target, next) {
						matched = true
					}
				}
			}
			if !matched {
				return ErrCorruptRecord
			}
		}
		for actionID := range record.Resolutions {
			found := false
			for _, choice := range d.Actions {
				if choice.ActionID == actionID && (choice.Decision == "retain-incomplete" || choice.Decision == "retain-replacement-incomplete") {
					found = true
				}
			}
			if !found {
				return ErrCorruptRecord
			}
		}
		for _, choice := range d.Actions {
			old, ok := record.Actions[choice.ActionID]
			if choice.Decision == "retain-incomplete" || choice.Decision == "retain-replacement-incomplete" {
				if _, transferred := record.Transfers[choice.ActionID]; transferred {
					// Target proof was checked above and its obligation is checked in this
					// same aggregate; increasing serials make this a finite chain.
				} else if resolution, resolved := record.Resolutions[choice.ActionID]; resolved {
					if !validIntentRecoveryResolution(record, choice, resolution) {
						return ErrCorruptRecord
					}
					if current, exists := a.Actions[resolution.ID]; exists && evidenceHash(current) != evidenceHash(resolution) {
						return ErrCorruptRecord
					}
				} else {
					resource := a.State.FindResource(old.Resource)
					expected := old.Candidate
					if choice.Decision == "retain-replacement-incomplete" {
						expected = old.Replacement
					}
					if resource == nil || a.State.TopologyUID != record.Observation.TopologyUID || resource.OwnerUID != record.Observation.TopologyUID || resource.ResourceUID != choice.ResourceUID || resource.Generation != choice.Generation || resource.ExternalID != choice.ExternalID || expected == nil || resource.Driver != expected.Driver || !sameIntentProviderState(*resource, *expected) || !resource.RecoveryRequired() {
						return ErrCorruptRecord
					}
				}
			}
			if !ok || old.DispositionID != "" {
				return ErrCorruptRecord
			}
			if current, exists := a.Actions[choice.ActionID]; exists {
				if current.DispositionID != "intent:"+id {
					return ErrCorruptRecord
				}
				current.DispositionID = ""
				if evidenceHash(old) != evidenceHash(current) {
					return ErrCorruptRecord
				}
			}
		}
	}
	for id, action := range a.Actions {
		if !strings.HasPrefix(action.DispositionID, "intent:") {
			continue
		}
		record, ok := a.IntentDispositions[strings.TrimPrefix(action.DispositionID, "intent:")]
		if !ok {
			return ErrCorruptRecord
		}
		found := false
		for _, choice := range record.Decision.Actions {
			if choice.ActionID == id {
				found = true
			}
		}
		if !found {
			return ErrCorruptRecord
		}
	}
	return nil
}

func validIntentRecoveryResolution(record IntentDispositionArchive, choice controlplane.IntentDispositionAction, action runtime.ActionRecord) bool {
	old, ok := record.Actions[choice.ActionID]
	if !ok || validateAction(action) != nil || action.Revision < 1 || action.Status != runtime.ActionSucceeded || action.DispositionID != "" || action.OwnerUID != record.Observation.TopologyUID || action.ResourceUID != choice.ResourceUID || !action.Resource.Equal(old.Resource) || action.ExpectedSerial < record.Decision.ExpectedSerial+1 || action.Candidate == nil || action.Candidate.OwnerUID != action.OwnerUID || action.Candidate.ResourceUID != action.ResourceUID {
		return false
	}
	expected := old.Candidate
	if choice.Decision == "retain-replacement-incomplete" {
		expected = old.Replacement
	}
	if expected == nil || action.Candidate.Driver != expected.Driver {
		return false
	}
	switch action.Action {
	case controlplane.PlanActionDelete:
		return action.Generation == choice.Generation && action.Candidate.Generation == choice.Generation && action.Candidate.ExternalID == choice.ExternalID && sameIntentProviderState(*action.Candidate, *expected)
	case controlplane.PlanActionReset:
		return action.Generation > choice.Generation && action.Candidate.Generation == action.Generation && action.Phase == "verifying" && action.EffectComplete && !action.Candidate.RecoveryRequired()
	}
	return false
}

// Called only by the atomic successful journal/resource-patch transition.
// Metadata writers cannot manufacture this resolution by removing a resource.
func resolveIntentRecoveryRequirements(a *aggregate, action runtime.ActionRecord) {
	if action.Status != runtime.ActionSucceeded {
		return
	}
	for id, record := range a.IntentDispositions {
		for _, choice := range record.Decision.Actions {
			if choice.Decision != "retain-incomplete" && choice.Decision != "retain-replacement-incomplete" {
				continue
			}
			if _, done := record.Resolutions[choice.ActionID]; done {
				continue
			}
			if _, done := record.Transfers[choice.ActionID]; done {
				continue
			}
			if validIntentRecoveryResolution(record, choice, action) {
				if record.Resolutions == nil {
					record.Resolutions = map[string]runtime.ActionRecord{}
				}
				record.Resolutions[choice.ActionID] = action
			}
		}
		a.IntentDispositions[id] = record
	}
}

func sameIntentProviderState(left, right state.Resource) bool {
	if evidenceHash(left.Attachments) != evidenceHash(right.Attachments) {
		return false
	}
	a, err := left.ProviderState()
	if err != nil {
		return false
	}
	b, err := right.ProviderState()
	if err != nil {
		return false
	}
	var first, second any
	firstDecoder := json.NewDecoder(bytes.NewReader(a))
	firstDecoder.UseNumber()
	secondDecoder := json.NewDecoder(bytes.NewReader(b))
	secondDecoder.UseNumber()
	if firstDecoder.Decode(&first) != nil || secondDecoder.Decode(&second) != nil {
		return false
	}
	return evidenceHash(first) == evidenceHash(second)
}
