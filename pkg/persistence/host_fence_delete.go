package persistence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/substrate"
)

// FenceDeleteReconciliation is a write-ahead decision. It contains no provider
// private state. A pending decision retains exclusion until every postimage is
// durable; retries accept only the exact preimages or planned postimages.
type FenceDeleteReconciliation struct {
	Key                  string                         `json:"key"`
	ObservationCommandID string                         `json:"observation_command_id"`
	HostPublicKey        []byte                         `json:"host_public_key"`
	Observation          controlplane.RunRecoveryReport `json:"observation"`
	BeforeLifecycle      string                         `json:"before_lifecycle"`
	AfterLifecycle       string                         `json:"after_lifecycle"`
	BeforeRun            string                         `json:"before_run"`
	AfterRun             string                         `json:"after_run"`
	BeforeCommand        string                         `json:"before_command"`
	AfterCommand         string                         `json:"after_command"`
	DecidedAt            time.Time                      `json:"decided_at"`
	Completed            bool                           `json:"completed"`
}
type FenceDeleteTransition struct {
	PreparedAuthority []byte
	Lifecycle         []byte
	Run               controlplane.Run
	Command           CommandRecord
	Completed         bool
}

func evidenceHash(v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func actionValues(a *aggregate) []runtime.ActionRecord {
	out := make([]runtime.ActionRecord, 0, len(a.Actions))
	for _, v := range a.Actions {
		out = append(out, v)
	}
	return out
}

// FenceJournal preserves the original admission wire encoding.
func FenceJournal(run controlplane.Run, serial int64, actions []runtime.ActionRecord) ([]runtime.ActionRecord, string, error) {
	selected := []runtime.ActionRecord{}
	for _, a := range actions {
		if a.RunID != run.ID {
			continue
		}
		if !a.RunQueuedAt.Equal(run.QueuedAt) || a.RunRevision != run.Revision {
			return nil, "", ErrConflict
		}
		selected = append(selected, a)
	}
	if len(selected) == 0 {
		return nil, "", ErrConflict
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	return selected, evidenceHash(struct {
		Run     controlplane.Run
		Serial  int64
		Actions []runtime.ActionRecord
	}{DurableRun(run), serial, selected}), nil
}
func fenceDeleteStopID(f HostFenceRecord) string {
	return "fence-delete:" + f.Request.ID + ":" + evidenceHash(f.Reconciliation)
}

func PrepareFenceDelete(authorityRaw, lifecycleRaw []byte, run controlplane.Run, original, observation CommandRecord, hostKey ed25519.PublicKey, request HostFenceReleaseRequest, now time.Time) (FenceDeleteTransition, error) {
	var out FenceDeleteTransition
	if request.Key == "" || len(request.Key) > 128 || !topologyName.MatchString(request.FenceID) || !topologyName.MatchString(request.ObservationCommandID) {
		return out, ErrInvalidRecord
	}
	ledger, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return out, err
	}
	fence, ok := ledger.Fences[request.FenceID]
	if !ok {
		return out, ErrNotFound
	}
	if fence.Reconciliation != nil {
		rec := fence.Reconciliation
		if rec.Key != request.Key || rec.ObservationCommandID != request.ObservationCommandID {
			return out, ErrConflict
		}
		if rec.Completed {
			return FenceDeleteTransition{PreparedAuthority: authorityRaw, Completed: true}, nil
		}
		a, err := decodeAggregate(lifecycleRaw)
		if err != nil {
			return out, err
		}
		if err = validateAggregate(a, fence.Request.Topology); err != nil {
			return out, err
		}
		if evidenceHash(a) != rec.AfterLifecycle {
			if evidenceHash(a) != rec.BeforeLifecycle {
				return out, ErrConflict
			}
			if err = applyFenceDeleteActions(a, fence, rec); err != nil {
				return out, err
			}
		}
		if evidenceHash(DurableRun(run)) != rec.AfterRun {
			if evidenceHash(DurableRun(run)) != rec.BeforeRun {
				return out, ErrConflict
			}
			run = fenceDeleteRun(run, rec.DecidedAt)
		}
		if evidenceHash(original) != rec.AfterCommand {
			if evidenceHash(original) != rec.BeforeCommand {
				return out, ErrConflict
			}
			original = fenceDeleteCommand(original, rec.DecidedAt)
		}
		if evidenceHash(a) != rec.AfterLifecycle || evidenceHash(DurableRun(run)) != rec.AfterRun || evidenceHash(original) != rec.AfterCommand {
			return out, ErrConflict
		}
		raw, err := json.Marshal(a)
		return FenceDeleteTransition{PreparedAuthority: authorityRaw, Lifecycle: raw, Run: run, Command: original}, err
	}
	if fence.IntentDisposition != nil || fence.Release != nil || fence.Receipt == nil || now.IsZero() || now.Before(ledger.ObservedAt) || now.Before(fence.Receipt.ObservedAt) {
		return out, ErrConflict
	}
	a, err := decodeAggregate(lifecycleRaw)
	if err != nil {
		return out, err
	}
	if err = validateAggregate(a, fence.Request.Topology); err != nil {
		return out, err
	}
	if run.ID != fence.Request.RunID || run.AgentID != fence.Request.Grant.AgentID || run.Topology != fence.Request.Topology || run.Op != "destroy" || run.Target != "" || uint64(run.Attempt) != fence.Request.Grant.Attempt || run.HostCompletion != nil || run.Cleanup != nil || (run.Status != controlplane.RunRunning && run.Status != controlplane.RunFailed) {
		return out, ErrConflict
	}
	_, journal, err := FenceJournal(run, a.Serial, actionValues(a))
	if err != nil || journal != fence.Request.JournalSHA256 {
		return out, ErrConflict
	}
	c := original.Command
	if original.Retired || c.ID != fence.Request.Grant.CommandID || c.Type != "run_assigned" || c.AgentID != run.AgentID || c.ActionID != run.ID || c.Run == nil || !SameRunIncarnation(*c.Run, run) || uint64(c.Attempt) != fence.Request.Grant.Attempt || c.TopologyUID != a.State.TopologyUID {
		return out, ErrConflict
	}
	// No child/other outstanding effects can be covered by this parent-only proof.
	for _, g := range ledger.Grants {
		if g.State != controlplane.GrantStopped && !sameGrantEpoch(g, fence.Request.Grant) {
			return out, ErrConflict
		}
	}
	current, ok := ledger.Grants[grantScopeKey(fence.Request.Grant.Scope)]
	if !ok || !sameGrantEpoch(current, fence.Request.Grant) || current.State != controlplane.GrantRevoked {
		return out, ErrConflict
	}
	obs := observation.Command
	report := observation.Recovery
	if !observation.Retired || obs.Status != controlplane.AgentCommandStatusCompleted || obs.ID != request.ObservationCommandID || obs.Type != controlplane.RunRecoveryCommand || obs.RecoveryMode != controlplane.RunRecoveryObserve || obs.ActionID != fence.Request.ID || obs.Run == nil || !SameRunIncarnation(*obs.Run, run) || obs.Run.Attempt != run.Attempt || obs.Run.LeaseOwner != run.LeaseOwner || obs.TopologyUID != a.State.TopologyUID || obs.ExpectedSerial != a.Serial || validateRecoveryReport(obs, report) != nil {
		return out, ErrConflict
	}
	digest, _ := fence.Request.Digest()
	if controlplane.VerifyFenceObservation(hostKey, *report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}, obs.LeaseOwner, obs.Attempt) != nil || report.StateSerial != a.Serial || report.TopologyUID != a.State.TopologyUID || report.ObservedAt.Before(fence.Receipt.ObservedAt) || report.ObservedAt.After(now) {
		return out, ErrConflict
	}
	rec := &FenceDeleteReconciliation{Key: request.Key, ObservationCommandID: obs.ID, HostPublicKey: append([]byte(nil), hostKey...), Observation: *report, BeforeLifecycle: evidenceHash(a), BeforeRun: evidenceHash(DurableRun(run)), BeforeCommand: evidenceHash(original), DecidedAt: now.UTC()}
	if err = applyFenceDeleteActions(a, fence, rec); err != nil {
		return out, err
	}
	run = fenceDeleteRun(run, rec.DecidedAt)
	original = fenceDeleteCommand(original, rec.DecidedAt)
	rec.AfterLifecycle = evidenceHash(a)
	rec.AfterRun = evidenceHash(DurableRun(run))
	rec.AfterCommand = evidenceHash(original)
	fence.Reconciliation = rec
	ledger.Fences[request.FenceID] = fence
	ledger.ObservedAt = now.UTC()
	prepared, err := json.Marshal(ledger)
	if err != nil {
		return out, err
	}
	if _, err = decodeExecutionAuthority(prepared); err != nil {
		return out, err
	}
	life, err := json.Marshal(a)
	return FenceDeleteTransition{PreparedAuthority: prepared, Lifecycle: life, Run: run, Command: original}, err
}
func fenceDeleteRun(run controlplane.Run, at time.Time) controlplane.Run {
	// The interrupted execution did not return success. Verified deletion is a
	// separate reconciliation outcome, never an invented original execution result.
	run.Status = controlplane.RunFailed
	run.Recoverable = false
	run.EndedAt = at
	run.LeaseUntil = time.Time{}
	run.Err = "execution fenced; deletion reconciled from signed absence evidence"
	return DurableRun(run)
}
func fenceDeleteCommand(cmd CommandRecord, at time.Time) CommandRecord {
	cmd.Retired = true
	cmd.Command.Status = controlplane.AgentCommandStatusFailed
	cmd.Command.EndedAt = at
	cmd.Command.Err = "execution fenced; deletion reconciled"
	return cmd
}
func applyFenceDeleteActions(a *aggregate, fence HostFenceRecord, rec *FenceDeleteReconciliation) error {
	if a.State.TopologyUID != fence.Request.Grant.Scope.TopologyUID || gcPending(a) || len(a.ResetContinuations) != 0 {
		return ErrConflict
	}
	selected := map[string]runtime.ActionRecord{}
	for _, action := range a.Actions {
		if action.RunID != fence.Request.RunID {
			if action.Status != runtime.ActionSucceeded {
				return ErrConflict
			}
			continue
		}
		selected[action.ID] = action
	}
	if len(selected) == 0 || len(selected) != len(rec.Observation.Resources) {
		return ErrConflict
	}
	for _, observed := range rec.Observation.Resources {
		action, ok := selected[observed.ActionID]
		if !ok || action.Action != model.PlanActionDelete || action.Resource.Type != "sysbox_node" || (action.Status != runtime.ActionUncertain && action.Status != runtime.ActionIntentCommitted) || action.ProviderOperationID != action.ID || action.ProviderOperationAnchor != "action:"+action.ID || action.Quiescence != "unknown" || action.ReplayForbidden || len(action.ResetSSH) != 0 || action.Replacement != nil {
			return ErrConflict
		}
		candidate := action.Candidate
		if candidate == nil || (candidate.Driver != "docker" && candidate.Driver != "libvirt") || candidate.ExternalID == "" || len(candidate.Attachments) != 0 || candidate.OwnerUID != action.OwnerUID || candidate.ResourceUID != action.ResourceUID || candidate.Generation != action.Generation {
			return ErrConflict
		}
		if observed.Status != "absent" || observed.ProviderScope != "local-only" || !observed.DeleteResidueAbsent || observed.JournalStatus != string(action.Status) || observed.ExternalID != candidate.ExternalID || observed.OwnerUID != action.OwnerUID || observed.ResourceUID != action.ResourceUID || observed.Generation != action.Generation || observed.Resource != action.Resource.String() {
			return ErrConflict
		}
		current := a.State.FindResource(action.Resource)
		if current == nil || current.OwnerUID != candidate.OwnerUID || current.ResourceUID != candidate.ResourceUID || current.Generation != candidate.Generation || current.ExternalID != candidate.ExternalID || current.Driver != candidate.Driver || len(current.Attachments) != 0 {
			return ErrConflict
		}
		action.Status = runtime.ActionSucceeded
		action.EffectComplete = true
		action.Error = ""
		action.Quiescence = "proven"
		evidence := map[string]any{"kind": "isolated_host_fence", "fence_id": fence.Request.ID, "controller_receipt_sha256": evidenceHash(fence.Receipt), "host_observation_sha256": evidenceHash(rec.Observation)}
		proof := substrate.QuiescenceProof{OperationID: action.ProviderOperationID, Anchor: action.ProviderOperationAnchor, Identity: substrate.OperationIdentity{ActionID: action.ID, OwnerUID: action.OwnerUID, TopologyUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: action.Generation}, Evidence: evidence, Digest: "sha256:" + evidenceHash(evidence)}
		action.QuiescenceProof = &proof
		// Original intent serial stays bound; outcome CAS uses the current locked
		// serial so a multi-delete batch advances atomically and deterministically.
		action.ExpectedSerial = a.Serial
		if err := applyActionOutcome(a, action, &runtime.StatePatch{Resource: action.Resource.String(), Action: action.Action, Op: runtime.StatePatchDelete}); err != nil {
			return err
		}
		delete(selected, observed.ActionID)
	}
	if len(selected) != 0 || len(a.State.Resources) != 0 {
		return ErrConflict
	}
	a.UpdatedAt = rec.DecidedAt
	return validateAggregate(a, fence.Request.Topology)
}
func FinishFenceDelete(authorityRaw, lifecycleRaw []byte, run controlplane.Run, cmd CommandRecord, request HostFenceReleaseRequest) ([]byte, error) {
	ledger, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	fence, ok := ledger.Fences[request.FenceID]
	if !ok || fence.Reconciliation == nil {
		return nil, ErrConflict
	}
	rec := fence.Reconciliation
	if rec.Key != request.Key || rec.ObservationCommandID != request.ObservationCommandID {
		return nil, ErrConflict
	}
	a, err := decodeAggregate(lifecycleRaw)
	if err != nil {
		return nil, err
	}
	if evidenceHash(a) != rec.AfterLifecycle || evidenceHash(DurableRun(run)) != rec.AfterRun || evidenceHash(cmd) != rec.AfterCommand {
		return nil, ErrConflict
	}
	if rec.Completed {
		return authorityRaw, nil
	}
	rec.Completed = true
	fence.Reconciliation = rec
	ledger.Fences[request.FenceID] = fence
	grant, ok := ledger.Grants[grantScopeKey(fence.Request.Grant.Scope)]
	if !ok || !sameGrantEpoch(grant, fence.Request.Grant) || grant.State != controlplane.GrantRevoked {
		return nil, ErrConflict
	}
	grant.State = controlplane.GrantStopped
	grant.StopReceipt = fenceDeleteStopID(fence)
	ledger.Grants[grantScopeKey(grant.Scope)] = grant
	if ledger.Stops == nil {
		ledger.Stops = map[uint64]stoppedExecutionGrant{}
	}
	ledger.Stops[grant.Epoch] = stoppedExecutionGrant{Grant: grant, Receipt: grant.StopReceipt}
	raw, err := json.Marshal(ledger)
	if err != nil {
		return nil, err
	}
	if _, err = decodeExecutionAuthority(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
func validateFenceDelete(fence HostFenceRecord, a *executionAuthority) error {
	r := fence.Reconciliation
	if r == nil {
		return nil
	}
	if fence.Receipt == nil || r.Key == "" || len(r.Key) > 128 || r.ObservationCommandID != r.Observation.CommandID || r.DecidedAt.Before(fence.Receipt.ObservedAt) || r.DecidedAt.After(a.ObservedAt) || r.Observation.ObservedAt.Before(fence.Receipt.ObservedAt) || r.Observation.ObservedAt.After(r.DecidedAt) || r.Observation.AgentID != fence.Request.Grant.AgentID || r.Observation.RunID != fence.Request.RunID || r.Observation.TopologyUID != fence.Request.Grant.Scope.TopologyUID || r.Observation.HostObservation == nil {
		return ErrCorruptRecord
	}
	for _, hash := range []string{r.BeforeLifecycle, r.AfterLifecycle, r.BeforeRun, r.AfterRun, r.BeforeCommand, r.AfterCommand} {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != hash {
			return ErrCorruptRecord
		}
	}
	digest, _ := fence.Request.Digest()
	p := r.Observation.HostObservation
	if controlplane.VerifyFenceObservation(r.HostPublicKey, r.Observation, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}, p.DeliveryOwner, p.DeliveryAttempt) != nil {
		return ErrCorruptRecord
	}
	for _, v := range r.Observation.Resources {
		if v.Status != "absent" || v.ProviderScope != "local-only" || !v.DeleteResidueAbsent || v.ExternalID == "" {
			return ErrCorruptRecord
		}
	}
	if r.Completed {
		stop, ok := a.Stops[fence.Request.Grant.Epoch]
		if !ok || !sameGrantEpoch(stop.Grant, fence.Request.Grant) || stop.Receipt != fenceDeleteStopID(fence) {
			return ErrCorruptRecord
		}
	} else {
		if fence.Release != nil {
			return ErrCorruptRecord
		}
	}
	return nil
}
