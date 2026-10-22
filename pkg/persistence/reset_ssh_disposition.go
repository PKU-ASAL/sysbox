package persistence

import (
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// This transition is used only after the complete signed observation and child
// stop ledger have been checked. It publishes no successful Run outcome.
func publishResetSSHDisposition(a *aggregate, action runtime.ActionRecord, at time.Time, addresses map[string]bool) error {
	if !action.ResetSSHResultsRecorded() || action.ValidateResetSSH(a.State) != nil || addresses[action.Resource.String()] {
		return ErrConflict
	}
	source := a.State.FindResource(action.Resource)
	if source == nil || source.OwnerUID != action.OwnerUID || source.ResourceUID != action.ResourceUID || source.Generation != action.Generation-1 || source.Driver != action.Candidate.Driver || source.ExternalID == "" || source.ExternalID == action.Candidate.ExternalID || len(source.Attachments) > 0 || source.RecoveryRequired() || action.Candidate.RecoveryRequired() {
		return ErrConflict
	}
	// The pinned SSH records bind the old provider identity even though the
	// Reset's Candidate has already advanced to the replacement generation.
	for _, dep := range action.ResetSSH {
		var saved struct {
			Handle struct {
				NodeID string `json:"node_id"`
			} `json:"handle"`
		}
		raw, err := json.Marshal(dep.Previous.Attributes["ssh_access"])
		if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Handle.NodeID != source.ExternalID || addresses[dep.Previous.Address.String()] {
			return ErrConflict
		}
		for _, other := range a.Actions {
			if other.ID != action.ID && !runtime.ActionSettled(other) && other.Resource.Equal(dep.Previous.Address) {
				return ErrConflict
			}
		}
		for _, resource := range a.State.Resources {
			if !resource.Address.Equal(dep.Result.Address) && resource.ResourceUID == dep.Result.ResourceUID {
				return ErrConflict
			}
		}
	}
	for _, resource := range a.State.Resources {
		if !resource.Address.Equal(action.Resource) && (resource.ResourceUID == action.ResourceUID || resource.Driver == action.Candidate.Driver && resource.ExternalID == action.Candidate.ExternalID) {
			return ErrConflict
		}
	}
	retained := *action.Candidate
	if err := retained.DeleteRuntimeValue("reset_handle"); err != nil {
		return err
	}
	retained.Status = state.ResourceUnknown
	if retained.CreatedAt.IsZero() {
		retained.CreatedAt = at
	}
	a.State.RemoveResource(action.Resource)
	a.State.AddResource(retained)
	a.State.FindResource(action.Resource).UpdatedAt = at
	addresses[action.Resource.String()] = true
	for _, dep := range action.ResetSSH {
		result := *dep.Result
		result.Status = state.ResourceUnknown
		if result.CreatedAt.IsZero() {
			result.CreatedAt = at
		}
		a.State.RemoveResource(result.Address)
		a.State.AddResource(result)
		a.State.FindResource(result.Address).UpdatedAt = at
		addresses[result.Address.String()] = true
	}
	return nil
}
