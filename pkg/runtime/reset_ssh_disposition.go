package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/substrate"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

// ResetSSHResultsRecorded is a journal shape, not evidence of cessation or
// current presence. It covers only the post-setup/pre-publication Reset window.
func (a ActionRecord) ResetSSHResultsRecorded() bool { return a.resetSSHDispositionShape(false) }

// ResetSSHIncompleteSupported identifies replacement-local SSH uncertainty that
// can be retained under a mandatory node rebuild/delete obligation.
func (a ActionRecord) ResetSSHIncompleteSupported() bool { return a.resetSSHDispositionShape(true) }

func (a ActionRecord) resetSSHDispositionShape(incomplete bool) bool {
	if a.Action != controlplane.PlanActionReset || a.Phase != "verifying" || !a.EffectComplete || len(a.ResetSSH) == 0 || a.Generation < 2 || a.Candidate == nil || a.Candidate.Generation != a.Generation || a.Candidate.OwnerUID != a.OwnerUID || a.Candidate.ResourceUID != a.ResourceUID || !a.Candidate.Address.Equal(a.Resource) || a.Candidate.ExternalID == "" || (!incomplete && len(a.Candidate.Attachments) > 0) {
		return false
	}
	if a.Replacement != nil && (a.Replacement.ExternalID != a.Candidate.ExternalID || a.Replacement.Generation != a.Generation || a.Replacement.OwnerUID != a.OwnerUID || a.Replacement.ResourceUID != a.ResourceUID || a.Replacement.Driver != a.Candidate.Driver || !a.Replacement.Address.Equal(a.Resource)) {
		return false
	}
	seen := map[string]bool{}
	for _, dep := range a.ResetSSH {
		if (!incomplete && (!dep.Started || dep.Result == nil)) || dep.Previous.OwnerUID != a.OwnerUID || dep.Previous.Driver != "managed" || len(dep.Previous.Attachments) > 0 || dep.Previous.RecoveryRequired() || dep.ActionID != resetSSHActionID(a.ID, dep.Previous.Address.String()) || seen[dep.Previous.Address.String()] || dep.Previous.Generation < 1 || dep.Previous.Generation == int64(^uint64(0)>>1) {
			return false
		}
		seen[dep.Previous.Address.String()] = true
		r := dep.Result
		if r == nil && incomplete {
			continue
		}
		if r.OwnerUID != a.OwnerUID || r.ResourceUID != dep.Previous.ResourceUID || r.Generation != dep.Previous.Generation+1 || !r.Address.Equal(dep.Previous.Address) || r.Driver != "managed" || len(r.Attachments) > 0 || r.RecoveryRequired() {
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
	resolved, err := ResetSSHWithObservedResults(a, observations)
	if err == nil {
		a = resolved
	}
	incomplete := !a.ResetSSHResultsRecorded()
	if incomplete && !a.ResetSSHIncompleteSupported() {
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
	if old == nil || old.ActionID != a.ID || old.Resource != a.Resource.String() || old.OwnerUID != a.OwnerUID || old.ResourceUID != a.ResourceUID || old.Generation != a.Generation-1 || old.ExternalID == "" || old.ExternalID == a.Candidate.ExternalID || old.Status != "absent" || old.ProviderScope != "local-only" || !old.DeleteResidueAbsent || (old.AttachmentCount > 0 && !old.AttachmentResidueAbsent) || old.JournalStatus != string(a.Status) || old.ResetSource != nil || old.ResetReplacement != nil || len(old.ResetSSH) != 0 {
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
			expected := dep.Result
			if expected == nil {
				r := dep.Previous
				r.Generation++
				r.ExternalID = dep.Config.AccessID
				expected = &r
			}
			if seen[child.ActionID] || !resetObservedIdentity(child, dep.ActionID, *expected, a.Status) || (!incomplete && child.Status != "present") || (child.Status != "present" && child.Status != "unknown" && child.Status != "absent") || child.ResetSource != nil || child.ResetReplacement != nil || len(child.ResetSSH) != 0 {
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
	if incomplete {
		p.ResourceDecision = "retain-incomplete-reset"
	}
	return p
}
func resetObservedIdentity(o controlplane.RecoveredResource, id string, r state.Resource, status ActionStatus) bool {
	return o.ActionID == id && o.Resource == r.Address.String() && o.OwnerUID == r.OwnerUID && o.ResourceUID == r.ResourceUID && o.Generation == r.Generation && o.ExternalID == r.ExternalID && o.JournalStatus == string(status)
}

func observeResetSSHDisposition(ctx context.Context, a ActionRecord, st *state.State, item *controlplane.RecoveredResource) {
	if a.Action != controlplane.PlanActionReset || a.Phase != "verifying" || !a.EffectComplete || a.Candidate == nil || len(a.ResetSSH) == 0 || a.ValidateResetSSH(st) != nil {
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
		var committed *substrate.SSHAccessResult
		if r == nil && dep.Started {
			id := substrate.OperationIdentity{ActionID: dep.ActionID, OwnerUID: a.OwnerUID, TopologyUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: a.Generation}
			childCtx := substrate.WithOperationIdentity(ctx, id)
			readCtx, connection, query, err := sshNodeConnection(childCtx, staged, a.Resource.String(), dep.Config.AccessID)
			if err == nil {
				found, err := connection.DiscoverSSHAccess(readCtx, query)
				if err == nil && found.Validate(query) == nil && found.Status == "present" && found.Current != nil {
					recovered, err := recoverResetSSHResult(a, dep, *found.Current)
					if err == nil {
						r = &recovered
						committed = found.Current
					}
				}
			}
		}
		if r == nil {
			item.ResetSSH = append(item.ResetSSH, controlplane.RecoveredResource{ActionID: dep.ActionID, Resource: dep.Previous.Address.String(), OwnerUID: a.OwnerUID, ResourceUID: dep.Previous.ResourceUID, Generation: dep.Previous.Generation + 1, ExternalID: dep.Config.AccessID, Status: "unknown", JournalStatus: string(a.Status)})
			continue
		}
		child := ActionRecord{ID: dep.ActionID, Resource: r.Address, OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation, Status: a.Status, Action: controlplane.PlanActionRead, Candidate: r}
		observed := observeIntentResource(ctx, child, r, staged)
		observed.SSHCommitted = committed
		item.ResetSSH = append(item.ResetSSH, observed)
	}
}

// ResetSSHWithObservedResults builds a detached publication candidate from exact
// committed read evidence. It never fills the original journal's missing Result.
// Configuration changes require reconstructed desired metadata to match the
// immutable admitted digest; the previous node reference alone grants nothing.
func ResetSSHWithObservedResults(a ActionRecord, observations []controlplane.RecoveredResource) (ActionRecord, error) {
	out := a
	out.ResetSSH = append([]ResetSSHDependency(nil), a.ResetSSH...)
	var observed *controlplane.RecoveredResource
	for i := range observations {
		if observations[i].ActionID != a.ID {
			continue
		}
		if observed != nil {
			return a, ErrObservationUnknown
		}
		observed = &observations[i]
	}
	for i, dep := range out.ResetSSH {
		if dep.Result != nil {
			continue
		}
		if !dep.Started || observed == nil {
			return a, ErrObservationUnknown
		}
		var child *controlplane.RecoveredResource
		for j := range observed.ResetSSH {
			if observed.ResetSSH[j].ActionID != dep.ActionID {
				continue
			}
			if child != nil {
				return a, ErrObservationUnknown
			}
			child = &observed.ResetSSH[j]
		}
		if child == nil || child.SSHCommitted == nil {
			return a, ErrObservationUnknown
		}
		result, err := recoverResetSSHResult(a, dep, *child.SSHCommitted)
		if err != nil || !resetObservedIdentity(*child, dep.ActionID, result, a.Status) || child.Status != "present" {
			return a, ErrObservationUnknown
		}
		out.ResetSSH[i].Result = &result
	}
	if !out.ResetSSHResultsRecorded() {
		return a, ErrObservationUnknown
	}
	return out, nil
}

func recoverResetSSHResult(a ActionRecord, dep ResetSSHDependency, committed substrate.SSHAccessResult) (state.Resource, error) {
	if a.Candidate == nil || !dep.Started || dep.Result != nil || dep.Previous.RecoveryRequired() || len(dep.Previous.Attachments) > 0 || committed.Handle.Revision == 0 {
		return state.Resource{}, ErrObservationUnknown
	}
	previous, err := decodeSSHState(dep.Previous, &state.State{TopologyUID: a.OwnerUID})
	if err != nil {
		return state.Resource{}, err
	}
	x, _ := json.Marshal(previous.Config)
	y, _ := json.Marshal(dep.Config)
	desired := map[string]any{}
	if old, ok := dep.Previous.Attributes[desiredPayloadKey].(map[string]any); ok {
		node, _ := old["node"].(string)
		cfg := &config.SSHAccessConfig{Node: node, AuthorizedKeys: append([]string(nil), dep.Config.AuthorizedKeys...), BindIP: dep.Config.BindIP, Port: dep.Config.Port}
		n := &graph.Node{Address: dep.Previous.Address, Data: cfg}
		ref, _, err := sshDesired(n, dep.Config.User, dep.ActionID)
		if err != nil || ref != a.Resource.String() || setDesiredHash(n, desired) != nil || desired[desiredHashKey] != dep.DesiredDigest {
			return state.Resource{}, fmt.Errorf("%w: admitted SSH desired digest mismatch", ErrObservationUnknown)
		}
	} else if string(x) != string(y) {
		return state.Resource{}, fmt.Errorf("%w: missing changed SSH desired metadata", ErrObservationUnknown)
	}
	req := substrate.SSHAccessMutation{ID: dep.ActionID, ExpectedRevision: committed.Handle.Revision - 1, Config: dep.Config}
	q := substrate.SSHAccessQuery{NodeID: a.Candidate.ExternalID, User: dep.Config.User, AccessID: dep.Config.AccessID, TopologyUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: a.Generation}
	if validateSSHMutationResult(q, req, committed) != nil {
		return state.Resource{}, ErrObservationUnknown
	}
	result := cloneResetStateResource(dep.Previous)
	result.Generation++
	result.Status = state.ResourceUnknown
	result.CreatedAt, result.UpdatedAt = time.Time{}, time.Time{}
	for key, value := range desired {
		result.Attributes[key] = value
	}
	result.Attributes["ssh_access"] = persistedSSHAccess{Version: 1, Node: a.Resource.String(), Config: dep.Config, Handle: committed.Handle}
	return result, nil
}
