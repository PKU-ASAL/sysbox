package runtime

import (
	"context"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

// ResetSSHResultsRecorded is a journal shape, not evidence of cessation or
// current presence. It covers only the post-setup/pre-publication Reset window.
func (a ActionRecord) ResetSSHResultsRecorded() bool {
	if a.Action != controlplane.PlanActionReset || a.Phase != "verifying" || !a.EffectComplete || len(a.ResetSSH) == 0 || a.Generation < 2 || a.Candidate == nil || a.Candidate.Generation != a.Generation || a.Candidate.OwnerUID != a.OwnerUID || a.Candidate.ResourceUID != a.ResourceUID || !a.Candidate.Address.Equal(a.Resource) || a.Candidate.ExternalID == "" || len(a.Candidate.Attachments) > 0 {
		return false
	}
	if a.Replacement != nil && (a.Replacement.ExternalID != a.Candidate.ExternalID || a.Replacement.Generation != a.Generation || a.Replacement.OwnerUID != a.OwnerUID || a.Replacement.ResourceUID != a.ResourceUID || a.Replacement.Driver != a.Candidate.Driver || !a.Replacement.Address.Equal(a.Resource)) {
		return false
	}
	seen := map[string]bool{}
	for _, dep := range a.ResetSSH {
		if !dep.Started || dep.Result == nil || dep.ActionID != resetSSHActionID(a.ID, dep.Previous.Address.String()) || seen[dep.Previous.Address.String()] || dep.Previous.Generation < 1 || dep.Previous.Generation == int64(^uint64(0)>>1) {
			return false
		}
		seen[dep.Previous.Address.String()] = true
		r := dep.Result
		if r.OwnerUID != a.OwnerUID || r.ResourceUID != dep.Previous.ResourceUID || r.Generation != dep.Previous.Generation+1 || !r.Address.Equal(dep.Previous.Address) || r.Driver != "managed" || len(r.Attachments) > 0 {
			return false
		}
		saved, err := decodeSSHState(*r, &state.State{TopologyUID: a.OwnerUID})
		if err != nil {
			return false
		}
		desired, _ := json.Marshal(dep.Config)
		actual, _ := json.Marshal(saved.Config)
		if string(desired) != string(actual) || saved.Node != a.Resource.String() || saved.Handle.ID != dep.ActionID || saved.Handle.NodeID != a.Candidate.ExternalID || saved.Handle.ResourceUID != a.ResourceUID || saved.Handle.Generation != a.Generation {
			return false
		}
	}
	return true
}

func planResetSSHDisposition(a ActionRecord, observations []controlplane.RecoveredResource, p IntentDispositionPlan) IntentDispositionPlan {
	p.ResourceDecision = "review-reset"
	if !a.ResetSSHResultsRecorded() {
		p.Blockers = append(p.Blockers, "reset_ssh_committed_results_required")
		return p
	}
	var observed *controlplane.RecoveredResource
	for i := range observations {
		if observations[i].ActionID == a.ID {
			if observed != nil {
				p.Blockers = append(p.Blockers, "observation_identity_or_coverage_mismatch")
				return p
			}
			observed = &observations[i]
		}
	}
	if observed == nil {
		p.Blockers = append(p.Blockers, "owning_agent_observation_required")
		return p
	}
	o := observed
	if !resetObservedIdentity(*o, a.ID, *a.Candidate, a.Status) || o.Status != "present" || o.ProviderScope != "local-only" || o.ResetReplacement != nil || len(o.ResetSSH) != len(a.ResetSSH) {
		p.Blockers = append(p.Blockers, "reset_ssh_observation_required")
		return p
	}
	old := o.ResetSource
	if old == nil || old.ActionID != a.ID || old.Resource != a.Resource.String() || old.OwnerUID != a.OwnerUID || old.ResourceUID != a.ResourceUID || old.Generation != a.Generation-1 || old.ExternalID == "" || old.ExternalID == a.Candidate.ExternalID || old.Status != "absent" || old.ProviderScope != "local-only" || !old.DeleteResidueAbsent || old.JournalStatus != string(a.Status) || old.ResetSource != nil || old.ResetReplacement != nil || len(old.ResetSSH) != 0 {
		p.Blockers = append(p.Blockers, "reset_ssh_old_source_absence_required")
		return p
	}
	seen := map[string]bool{}
	for _, dep := range a.ResetSSH {
		previous, err := decodeSSHState(dep.Previous, &state.State{TopologyUID: a.OwnerUID})
		if err != nil || previous.Handle.NodeID != old.ExternalID || previous.Handle.ResourceUID != a.ResourceUID || previous.Handle.Generation != old.Generation || previous.Node != a.Resource.String() {
			p.Blockers = append(p.Blockers, "reset_ssh_source_identity_mismatch")
			return p
		}
		found := false
		for _, child := range o.ResetSSH {
			if child.ActionID != dep.ActionID {
				continue
			}
			if seen[child.ActionID] || !resetObservedIdentity(child, dep.ActionID, *dep.Result, a.Status) || child.Status != "present" || child.ResetSource != nil || child.ResetReplacement != nil || len(child.ResetSSH) != 0 {
				p.Blockers = append(p.Blockers, "reset_ssh_observation_required")
				return p
			}
			seen[child.ActionID] = true
			found = true
		}
		if !found {
			p.Blockers = append(p.Blockers, "reset_ssh_observation_required")
			return p
		}
	}
	p.ResourceDecision = "retain-managed"
	return p
}
func resetObservedIdentity(o controlplane.RecoveredResource, id string, r state.Resource, status ActionStatus) bool {
	return o.ActionID == id && o.Resource == r.Address.String() && o.OwnerUID == r.OwnerUID && o.ResourceUID == r.ResourceUID && o.Generation == r.Generation && o.ExternalID == r.ExternalID && o.JournalStatus == string(status)
}

func observeResetSSHDisposition(ctx context.Context, a ActionRecord, st *state.State, item *controlplane.RecoveredResource) {
	if !a.ResetSSHResultsRecorded() || a.ValidateResetSSH(st) != nil {
		return
	}
	source := st.FindResource(a.Resource)
	if source == nil || source.OwnerUID != a.OwnerUID || source.ResourceUID != a.ResourceUID || source.Generation != a.Generation-1 || source.Driver != a.Candidate.Driver || source.ExternalID == a.Candidate.ExternalID {
		return
	}
	prior := a
	prior.Action = controlplane.PlanActionDelete
	prior.Generation = source.Generation
	prior.Candidate = source
	old := observeIntentResource(ctx, prior, source, st)
	item.ResetSource = &old
	staged, err := cloneExecutionState(st)
	if err != nil {
		return
	}
	staged.RemoveResource(a.Resource)
	staged.AddResource(*a.Candidate)
	for _, dep := range a.ResetSSH {
		r := dep.Result
		child := ActionRecord{ID: dep.ActionID, Resource: r.Address, OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation, Status: a.Status, Action: controlplane.PlanActionRead, Candidate: r}
		observed := observeIntentResource(ctx, child, r, staged)
		item.ResetSSH = append(item.ResetSSH, observed)
	}
}
