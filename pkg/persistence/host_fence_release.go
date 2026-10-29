package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
)

type HostFenceReleaseRequest struct {
	FenceID              string `json:"fence_id"`
	Key                  string `json:"key"`
	ObservationCommandID string `json:"observation_command_id"`
}
type HostFenceRelease struct {
	Key                  string                         `json:"key"`
	ObservationCommandID string                         `json:"observation_command_id"`
	LifecycleSHA256      string                         `json:"lifecycle_sha256"`
	ReleasedAt           time.Time                      `json:"released_at"`
	HostPublicKey        []byte                         `json:"host_public_key"`
	Observation          controlplane.RunRecoveryReport `json:"observation"`
}

// ReplayHostFenceRelease reads the immutable result before any current-state
// checks. A retry cannot release another hold or modify a new execution epoch.
func ReplayHostFenceRelease(raw []byte, request HostFenceReleaseRequest) (*HostFenceRelease, error) {
	if !topologyName.MatchString(request.FenceID) || request.Key == "" || len(request.Key) > 128 || !topologyName.MatchString(request.ObservationCommandID) {
		return nil, ErrInvalidRecord
	}
	a, err := decodeExecutionAuthority(raw)
	if err != nil {
		return nil, err
	}
	record, ok := a.Fences[request.FenceID]
	if !ok {
		return nil, ErrNotFound
	}
	if record.Release == nil {
		return nil, nil
	}
	if record.Release.Key != request.Key || record.Release.ObservationCommandID != request.ObservationCommandID {
		return nil, ErrConflict
	}
	return record.Release, nil
}

// ApplyHostFenceRelease changes only authority. Callers hold record/admission,
// authority and lifecycle exclusion and independently reject unresolved activity.
// This deliberately supports empty, already-reconciled delete topologies only.
func ApplyHostFenceRelease(authorityRaw, lifecycleRaw []byte, run controlplane.Run, command CommandRecord, hostKey ed25519.PublicKey, request HostFenceReleaseRequest, now time.Time) ([]byte, HostFenceRelease, error) {
	var zero HostFenceRelease
	prior, err := ReplayHostFenceRelease(authorityRaw, request)
	if err != nil {
		return nil, zero, err
	}
	if prior != nil {
		return authorityRaw, *prior, nil
	}
	a, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, zero, err
	}
	fence := a.Fences[request.FenceID]
	if fence.Receipt == nil || now.IsZero() || now.Before(a.ObservedAt) || now.Before(fence.Receipt.ObservedAt) {
		return nil, zero, ErrConflict
	}
	lifecycle, err := decodeAggregate(lifecycleRaw)
	if err != nil {
		return nil, zero, err
	}
	if err = validateAggregate(lifecycle, fence.Request.Topology); err != nil {
		return nil, zero, err
	}
	if lifecycle.State.TopologyUID != fence.Request.Grant.Scope.TopologyUID || len(lifecycle.State.Resources) != 0 || gcPending(lifecycle) || len(lifecycle.ResetContinuations) != 0 {
		return nil, zero, ErrConflict
	}
	if run.ID != fence.Request.RunID || run.Topology != fence.Request.Topology || run.AgentID != fence.Request.Grant.AgentID || !run.Status.IsTerminal() || run.QueuedAt.IsZero() || run.EndedAt.Before(run.QueuedAt) || run.EndedAt.IsZero() || uint64(run.Attempt) != fence.Request.Grant.Attempt || run.Op != "destroy" {
		return nil, zero, ErrConflict
	}
	if rec := fence.Reconciliation; rec != nil {
		if !rec.Completed || evidenceHash(lifecycle) != rec.AfterLifecycle || evidenceHash(DurableRun(run)) != rec.AfterRun || request.ObservationCommandID != rec.ObservationCommandID || command.Recovery == nil || evidenceHash(*command.Recovery) != evidenceHash(rec.Observation) || !command.Retired || command.Command.Status != controlplane.AgentCommandStatusCompleted {
			return nil, zero, ErrConflict
		}
		return commitFenceRelease(a, fence, lifecycleRaw, request, rec.HostPublicKey, rec.Observation, now)
	}
	c := command.Command
	if !command.Retired || c.Status != controlplane.AgentCommandStatusCompleted || c.ID != request.ObservationCommandID || c.Type != controlplane.RunRecoveryCommand || c.RecoveryMode != controlplane.RunRecoveryObserve || c.ActionID != request.FenceID || c.AgentID != run.AgentID || c.TopologyUID != lifecycle.State.TopologyUID || c.ExpectedSerial != lifecycle.Serial || c.Run == nil || !SameRunIncarnation(*c.Run, run) || c.Run.Attempt != run.Attempt || c.Run.LeaseOwner != run.LeaseOwner || validateRecoveryReport(c, command.Recovery) != nil {
		return nil, zero, ErrConflict
	}
	report := command.Recovery
	digest, err := fence.Request.Digest()
	if err != nil {
		return nil, zero, err
	}
	binding := controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}
	if controlplane.VerifyFenceObservation(hostKey, *report, binding, c.LeaseOwner, c.Attempt) != nil || report.TopologyUID != lifecycle.State.TopologyUID || report.StateSerial != lifecycle.Serial || report.ObservedAt.Before(fence.Receipt.ObservedAt) || report.ObservedAt.After(now) {
		return nil, zero, ErrConflict
	}
	selected := map[string]runtime.ActionRecord{}
	for _, action := range lifecycle.Actions {
		if action.Status != runtime.ActionSucceeded {
			return nil, zero, ErrConflict
		}
		if action.RunID != run.ID {
			continue
		}
		if action.Action != model.PlanActionDelete || action.OwnerUID != lifecycle.State.TopologyUID || !action.RunQueuedAt.Equal(run.QueuedAt) || action.RunRevision != run.Revision {
			return nil, zero, ErrConflict
		}
		selected[action.ID] = action
	}
	if len(selected) == 0 || len(selected) != len(report.Resources) {
		return nil, zero, ErrConflict
	}
	for _, observed := range report.Resources {
		action, ok := selected[observed.ActionID]
		if !ok || observed.Status != "absent" || observed.JournalStatus != string(runtime.ActionSucceeded) || observed.Resource != action.Resource.String() || observed.OwnerUID != action.OwnerUID || observed.ResourceUID != action.ResourceUID || observed.Generation != action.Generation {
			return nil, zero, ErrConflict
		}
		if action.Candidate == nil || action.Candidate.ExternalID == "" || observed.ExternalID != action.Candidate.ExternalID {
			return nil, zero, ErrConflict
		}
		delete(selected, observed.ActionID)
	}
	if len(selected) != 0 {
		return nil, zero, ErrConflict
	}
	return commitFenceRelease(a, fence, lifecycleRaw, request, hostKey, *report, now)
}
func commitFenceRelease(a *executionAuthority, fence HostFenceRecord, lifecycleRaw []byte, request HostFenceReleaseRequest, hostKey ed25519.PublicKey, report controlplane.RunRecoveryReport, now time.Time) ([]byte, HostFenceRelease, error) {
	var zero HostFenceRelease
	// Receipt is exclusion evidence, never an invented grant stop.
	stopped, ok := a.Stops[fence.Request.Grant.Epoch]
	if !ok || !sameGrantEpoch(stopped.Grant, fence.Request.Grant) {
		return nil, zero, ErrConflict
	}
	for _, g := range a.Grants {
		if (g.Scope.TopologyUID == fence.Request.Grant.Scope.TopologyUID || g.AgentID == fence.Request.Grant.AgentID) && g.State != controlplane.GrantStopped {
			return nil, zero, ErrConflict
		}
	}
	result := HostFenceRelease{Key: request.Key, ObservationCommandID: request.ObservationCommandID, LifecycleSHA256: fmt.Sprintf("%x", sha256.Sum256(lifecycleRaw)), ReleasedAt: now.UTC(), HostPublicKey: append([]byte(nil), hostKey...), Observation: report}
	fence.Release = &result
	a.Fences[request.FenceID] = fence
	barrier := a.Maintenance[fence.BarrierID]
	barrier.Released = true
	a.Maintenance[fence.BarrierID] = barrier
	a.ObservedAt = now.UTC()
	next, err := json.Marshal(a)
	if err != nil {
		return nil, zero, err
	}
	if _, err = decodeExecutionAuthority(next); err != nil {
		return nil, zero, err
	}
	return next, result, nil
}

func validateFenceRelease(record HostFenceRecord) error {
	release := record.Release
	if release == nil {
		return nil
	}
	if record.Receipt == nil || release.Key == "" || len(release.Key) > 128 || release.ObservationCommandID != release.Observation.CommandID || len(release.LifecycleSHA256) != 64 || release.ReleasedAt.Before(record.Receipt.ObservedAt) || release.Observation.ObservedAt.After(release.ReleasedAt) || release.Observation.ObservedAt.Before(record.Receipt.ObservedAt) || release.Observation.AgentID != record.Request.Grant.AgentID || release.Observation.RunID != record.Request.RunID || release.Observation.TopologyUID != record.Request.Grant.Scope.TopologyUID || release.Observation.HostObservation == nil {
		return ErrCorruptRecord
	}
	hash, err := hex.DecodeString(release.LifecycleSHA256)
	if err != nil || len(hash) != sha256.Size {
		return ErrCorruptRecord
	}
	if rec := record.Reconciliation; rec != nil {
		if !rec.Completed || evidenceHash(release.Observation) != evidenceHash(rec.Observation) {
			return ErrCorruptRecord
		}
	}
	seen := map[string]bool{}
	for _, resource := range release.Observation.Resources {
		if resource.ActionID == "" || seen[resource.ActionID] || resource.Status != "absent" || (record.Reconciliation == nil && resource.JournalStatus != "succeeded") || resource.OwnerUID != release.Observation.TopologyUID || resource.ExternalID == "" {
			return ErrCorruptRecord
		}
		seen[resource.ActionID] = true
	}
	digest, err := record.Request.Digest()
	if err != nil {
		return ErrCorruptRecord
	}
	proof := release.Observation.HostObservation
	if controlplane.VerifyFenceObservation(release.HostPublicKey, release.Observation, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: record.Receipt.NewBootID}, proof.DeliveryOwner, proof.DeliveryAttempt) != nil {
		return ErrCorruptRecord
	}
	return nil
}

// WithFenceReleaseSnapshot validates an aggregate under its lock while the
// caller retains authority exclusion. It does not change lifecycle contents.
func (r *Repository) WithFenceReleaseSnapshot(ctx context.Context, topology string, check func([]byte) error) error {
	return r.update(ctx, topology, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		return check(raw)
	})
}

// CommitFilesystemHostFenceRelease publishes authority while BOTH the authority
// and lifecycle locks remain held. Returning next bytes to the outer updater
// would release the lifecycle lock before publication and allow a direct writer
// to invalidate the checked snapshot in that gap.
func (r *Repository) CommitFilesystemHostFenceRelease(ctx context.Context, runsDir string, topology string, request HostFenceReleaseRequest, hostKey ed25519.PublicKey, load func() (controlplane.Run, CommandRecord, error)) (HostFenceRelease, error) {
	var result HostFenceRelease
	records := &fileGuestRecords{root: runsDir}
	err := records.updateRecord(ctx, executionAuthorityKind, "authority", func(authority []byte) ([]byte, error) {
		prior, err := ReplayHostFenceRelease(authority, request)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			result = *prior
			return nil, nil
		}
		run, cmd, err := load()
		if err != nil {
			return nil, err
		}
		err = r.WithFenceReleaseSnapshot(ctx, topology, func(raw []byte) error {
			next, release, err := ApplyHostFenceRelease(authority, raw, run, cmd, hostKey, request, time.Now().UTC())
			if err != nil {
				return err
			}
			dir, err := records.directory(executionAuthorityKind)
			if err != nil {
				return err
			}
			if err = writePrivateGuestRecord(ctx, filepath.Join(dir, "authority.json"), next); err != nil {
				return err
			}
			result = release
			return nil
		})
		return nil, err
	})
	return result, err
}
