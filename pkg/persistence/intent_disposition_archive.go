package persistence

import (
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
func intentDispositionPostimage(a *aggregate, d controlplane.IntentDispositionDecision, at time.Time) (map[string]runtime.ActionRecord, error) {
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
		if !ok || runtime.ActionSettled(action) || action.OwnerUID != next.State.TopologyUID || action.Candidate == nil || action.Replacement != nil || (action.Action == controlplane.PlanActionReset && !action.ResetBeforeReplacement()) || len(action.ResetSSH) > 0 || (action.ReplayForbidden && !action.EffectComplete && !action.ResetBeforeReplacement()) || addresses[action.Resource.String()] {
			return nil, ErrConflict
		}
		candidate := action.Candidate
		if choice.ResourceUID != action.ResourceUID || choice.Generation != action.Generation || choice.ExternalID != candidate.ExternalID || candidate.ExternalID == "" || len(candidate.Attachments) > 0 {
			return nil, ErrConflict
		}
		current := next.State.FindResource(action.Resource)
		if current != nil && (current.OwnerUID != candidate.OwnerUID || current.ResourceUID != candidate.ResourceUID || current.Generation != candidate.Generation || current.Driver != candidate.Driver || current.ExternalID != candidate.ExternalID || len(current.Attachments) > 0) {
			return nil, ErrConflict
		}
		for _, resource := range next.State.Resources {
			if !resource.Address.Equal(action.Resource) && (resource.ResourceUID == candidate.ResourceUID || (resource.Driver == candidate.Driver && resource.ExternalID == candidate.ExternalID)) {
				return nil, ErrConflict
			}
		}
		switch choice.Decision {
		case "retain":
			retained := *candidate
			if current != nil {
				retained = *current
			}
			var private map[string]json.RawMessage
			if json.Unmarshal(retained.Private, &private) != nil || len(private) == 0 {
				return nil, ErrConflict
			}
			if action.ResetBeforeReplacement() {
				if err := retained.DeleteRuntimeValue("reset_handle"); err != nil {
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
	if _, err = intentDispositionPostimage(a, d, decidedAt); err != nil {
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
		for _, choice := range d.Actions {
			old, ok := record.Actions[choice.ActionID]
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
