// Package persistence owns atomic lifecycle aggregates. Runtime defines its
// repository port; adapters do not implement provider effects or HTTP behavior.
package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	ErrConflict           = errors.New("repository conflict")
	ErrNotFound           = errors.New("repository record not found")
	ErrCorruptRecord      = errors.New("corrupt repository record")
	ErrUnsupportedVersion = errors.New("unsupported repository version")
)

const FormatVersion = 16

var topologyName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type aggregate struct {
	IntentDispositions   map[string]IntentDispositionArchive              `json:"intent_dispositions,omitempty"`
	AdoptionRecoveries   map[string]AdoptionRecoveryRecord                `json:"adoption_recoveries,omitempty"`
	OwnershipTargets     map[string]OwnershipTargetReservation            `json:"ownership_targets,omitempty"`
	OwnershipRetirements map[string]controlplane.OwnershipRetirementProof `json:"ownership_retirements,omitempty"`
	DeletedFrom          *LifecycleDeletionReceipt                        `json:"deleted_from,omitempty"`
	GCSources            map[string]GCSource                              `json:"gc_sources,omitempty"`
	GC                   map[string]GCReservation                         `json:"gc_reservations"`
	ResetContinuations   map[string]runtime.ResetContinuationReceipt      `json:"reset_continuations"`
	Restores             map[string]SnapshotRestoreReceipt                `json:"restores"`
	Snapshots            map[string]aggregateSnapshot                     `json:"snapshots"`
	Format               int                                              `json:"format"`
	Serial               int64                                            `json:"serial"`
	State                *state.State                                     `json:"state"`
	Actions              map[string]runtime.ActionRecord                  `json:"actions"`
	UpdatedAt            time.Time                                        `json:"updated_at"`
}
type backend interface {
	update(context.Context, string, func(*aggregate) error) error
	keys(context.Context) ([]string, error)
	close() error
	name() string
}
type Repository struct {
	backend             backend
	boundTopology       string
	borrowedBackend     bool
	externalGCMu        sync.RWMutex
	externalGCInventory func(context.Context, string) (map[string][]string, error)
}

// SetExternalGCInventoryProvider installs the explicit producer used to
// account for references that live outside this lifecycle repository (for
// example another host's revision, artifact, cache, or placement catalog).
// Configure before exposing the repository to callers. This is a read-only
// planning adapter: external snapshots deliberately have a different digest
// from the local authority snapshot, so they cannot authorize ReserveGC.
// Cross-host deletion requires durable catalog fencing, which this callback
// does not provide. A producer error makes GC reference loading fail closed.
func (r *Repository) SetExternalGCInventoryProvider(provider func(context.Context, string) (map[string][]string, error)) error {
	if provider == nil {
		return fmt.Errorf("%w: nil external GC inventory provider", ErrCorruptRecord)
	}
	r.externalGCMu.Lock()
	defer r.externalGCMu.Unlock()
	r.externalGCInventory = provider
	return nil
}

func (r *Repository) Close() error {
	if r.borrowedBackend {
		return nil
	}
	return r.backend.close()
}

// BindTopology borrows this repository's database pool without transferring
// ownership. The owner must outlive every view and close after users stop.
func (r *Repository) BindTopology(topology string) (*Repository, error) {
	if !topologyName.MatchString(topology) || r.boundTopology != "" && r.boundTopology != topology {
		return nil, ErrInvalidRecord
	}
	if _, ok := r.backend.(*sqlBackend); !ok {
		return nil, ErrUnsupportedVersion
	}
	return &Repository{backend: r.backend, boundTopology: topology, borrowedBackend: true}, nil
}

func (r *Repository) ListTopologies(ctx context.Context) ([]state.TopologyMetadata, error) {
	keys, err := r.backend.keys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.TopologyMetadata, 0, len(keys))
	for _, key := range keys {
		if r.boundTopology != "" && key != r.boundTopology {
			continue
		}
		if !topologyName.MatchString(key) {
			return nil, ErrCorruptRecord
		}
		err := r.update(ctx, key, func(a *aggregate) error {
			if a.Serial > 0 || len(a.Actions) > 0 || len(a.GCSources) > 0 {
				out = append(out, state.TopologyMetadata{Name: key, HasState: true, ResourceCount: len(a.State.Resources), Serial: a.Serial, Backend: r.backend.name(), UpdatedAt: a.UpdatedAt})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func newAggregate() *aggregate {
	return &aggregate{GC: map[string]GCReservation{}, Format: FormatVersion, State: &state.State{Version: state.SchemaVersion, TopologyUID: uuid.NewString()}, Actions: map[string]runtime.ActionRecord{}, Snapshots: map[string]aggregateSnapshot{}, Restores: map[string]SnapshotRestoreReceipt{}, ResetContinuations: map[string]runtime.ResetContinuationReceipt{}}
}
func decodeAggregate(raw []byte) (*aggregate, error) {
	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorruptRecord, err)
	}
	if header.Format != FormatVersion {
		return nil, fmt.Errorf("%w: got %d want %d", ErrUnsupportedVersion, header.Format, FormatVersion)
	}
	var a aggregate
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorruptRecord, err)
	}
	if a.Format != FormatVersion {
		return nil, fmt.Errorf("%w: got %d want %d", ErrUnsupportedVersion, a.Format, FormatVersion)
	}
	if err := validateAggregate(&a, ""); err != nil {
		return nil, err
	}
	return &a, nil
}

// Validate both loaded records and proposed mutations. JSON syntax alone does
// not make identity, journal revision, or recovery evidence trustworthy.
func validateAggregate(a *aggregate, topology string) error {
	if a.Format != FormatVersion {
		return ErrUnsupportedVersion
	}
	if err := validateIntentDispositionArchives(a); err != nil {
		return err
	}
	if err := validateAdoptionRecoveries(a); err != nil {
		return err
	}
	if a.GC == nil || a.State == nil || a.State.TopologyUID == "" || a.Actions == nil || a.Snapshots == nil || a.Restores == nil || a.ResetContinuations == nil || a.Serial < 0 {
		return ErrCorruptRecord
	}
	if a.DeletedFrom != nil && (a.DeletedFrom.TopologyUID == "" || a.DeletedFrom.TopologyUID == a.State.TopologyUID || a.DeletedFrom.Serial < 0 || a.DeletedFrom.Empty && (a.DeletedFrom.DeletedAt.IsZero() || a.DeletedFrom.Serial <= 0) || !a.DeletedFrom.DeletedAt.IsZero() && a.DeletedFrom.DeletedAt.After(a.UpdatedAt)) {
		return ErrCorruptRecord
	}
	if err := validateGCSources(a); err != nil {
		return err
	}
	if err := validateOwnershipTargets(a); err != nil {
		return err
	}
	if err := validateOwnershipRetirements(a); err != nil {
		return err
	}
	if err := validateGCReservations(a); err != nil {
		return err
	}
	for id, receipt := range a.Restores {
		if id != receipt.Request.ID || !validSnapshotRestoreRequest(receipt.Request) || receipt.Request.TopologyUID != a.State.TopologyUID || receipt.Serial != receipt.Request.ExpectedSerial+1 || receipt.Serial > a.Serial || receipt.CompletedAt.IsZero() {
			return ErrCorruptRecord
		}
	}
	if a.State.Version != state.SchemaVersion {
		return ErrUnsupportedVersion
	}
	addresses := map[string]bool{}
	uids := map[string]bool{}
	for _, resource := range a.State.Resources {
		if resource.Address.IsZero() || resource.OwnerUID != a.State.TopologyUID || resource.ResourceUID == "" || resource.Generation < 1 || addresses[resource.Address.String()] || uids[resource.ResourceUID] {
			return fmt.Errorf("%w: invalid resource identity", ErrCorruptRecord)
		}
		addresses[resource.Address.String()], uids[resource.ResourceUID] = true, true
	}
	for key, action := range a.Actions {
		if key != action.ID || validateAction(action) != nil || action.OwnerUID != a.State.TopologyUID || action.Revision < 1 || action.ExpectedSerial > a.Serial {
			return fmt.Errorf("%w: invalid action identity or revision", ErrCorruptRecord)
		}
		if topology == "" {
			topology = action.Topology
		}
		if action.Topology != topology {
			return fmt.Errorf("%w: action topology mismatch", ErrCorruptRecord)
		}
		switch action.Status {
		case runtime.ActionIntentCommitted, runtime.ActionSucceeded, runtime.ActionUncertain, runtime.ActionBlocked:
		default:
			return fmt.Errorf("%w: invalid action status", ErrCorruptRecord)
		}
		if action.Status == runtime.ActionSucceeded {
			if action.ExpectedSerial >= a.Serial || action.Action != model.PlanActionDelete && action.Candidate == nil {
				return fmt.Errorf("%w: success lacks atomic patch evidence", ErrCorruptRecord)
			}
		}
		if c := action.Candidate; c != nil {
			want := action.Generation
			// Reset checkpoints before replacement preserve the old generation
			// and its provider reset token, never pretend it is already new.
			if action.Action == model.PlanActionReset && !action.EffectComplete && action.Status != runtime.ActionSucceeded {
				want--
			}
			if !c.Address.Equal(action.Resource) || c.OwnerUID != action.OwnerUID || c.ResourceUID != action.ResourceUID || c.Generation != want || want < 1 {
				return fmt.Errorf("%w: action candidate identity mismatch", ErrCorruptRecord)
			}
		}
		if replacement := action.Replacement; replacement != nil {
			if action.Action != model.PlanActionReset || (action.Phase != "applying" && action.Phase != "verifying") || !replacement.Address.Equal(action.Resource) || replacement.OwnerUID != action.OwnerUID || replacement.ResourceUID != action.ResourceUID || replacement.Generation != action.Generation || replacement.ExternalID == "" {
				return fmt.Errorf("%w: invalid reset replacement identity", ErrCorruptRecord)
			}
			if action.EffectComplete && (action.Candidate == nil || action.Candidate.ExternalID != replacement.ExternalID || action.Candidate.Driver != replacement.Driver) {
				return fmt.Errorf("%w: completed reset differs from published replacement", ErrCorruptRecord)
			}
		}
	}
	for runID, receipt := range a.ResetContinuations {
		if runID != receipt.Request.Checkpoint.RunID || receipt.AdmittedAt.IsZero() || receipt.Request.TopologyUID != a.State.TopologyUID || receipt.Request.ExpectedSerial > a.Serial {
			return ErrCorruptRecord
		}
		members, err := resetContinuationMembers(receipt.Request)
		if err != nil {
			return ErrCorruptRecord
		}
		for id, original := range members {
			current, exists := a.Actions[id]
			if !exists || !sameIntent(original, current) {
				return ErrCorruptRecord
			}
		}
		for id, action := range a.Actions {
			if action.RunID == runID {
				if _, exists := members[id]; !exists {
					return ErrCorruptRecord
				}
			}
		}
	}
	return nil
}
func (r *Repository) update(ctx context.Context, topology string, fn func(*aggregate) error) error {
	return r.updateGC(ctx, topology, false, fn)
}

func (r *Repository) updateGC(ctx context.Context, topology string, gcTransition bool, fn func(*aggregate) error) error {
	if r.boundTopology != "" && topology != r.boundTopology {
		return fmt.Errorf("backend topology %q does not match requested topology %q", r.boundTopology, topology)
	}
	if !topologyName.MatchString(topology) {
		return fmt.Errorf("invalid topology %q", topology)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.backend.update(ctx, topology, func(a *aggregate) error {
		if err := validateAggregate(a, topology); err != nil {
			return err
		}
		var before []byte
		if (!gcTransition && gcPending(a)) || ownershipPending(a) {
			raw, err := json.Marshal(a)
			if err != nil {
				return err
			}
			before = raw
		}
		if err := fn(a); err != nil {
			return err
		}
		if before != nil {
			after, err := json.Marshal(a)
			if err != nil {
				return err
			}
			if string(before) != string(after) {
				return fmt.Errorf("%w: GC reservation freezes lifecycle references", ErrConflict)
			}
		}
		return validateAggregate(a, topology)
	})
}
func cloneState(st *state.State) (*state.State, error) {
	raw, err := st.Marshal()
	if err != nil {
		return nil, err
	}
	return state.Unmarshal(raw)
}
func (r *Repository) LoadState(ctx context.Context, topology string) (*state.State, error) {
	var out *state.State
	err := r.update(ctx, topology, func(a *aggregate) error {
		var err error
		out, err = r.snapshotState(a)
		return err
	})
	return out, err
}

func (r *Repository) snapshotState(a *aggregate) (*state.State, error) {
	out, err := cloneState(a.State)
	if err == nil {
		out.Meta = state.StateMeta{Backend: r.backend.name(), Serial: a.Serial, Exists: true, UpdatedAt: a.UpdatedAt}
	}
	return out, err
}

func snapshotActions(a *aggregate) ([]runtime.ActionRecord, error) {
	var out []runtime.ActionRecord
	for _, action := range a.Actions {
		out = append(out, action)
	}
	// Candidates contain pointers and opaque private state. Detach those from
	// the aggregate before returning the snapshot to callers.
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	out = nil
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LoadStateAndActions observes resource publication and its action outcome in
// one transaction, including the transition from an unfinished create candidate
// to a committed resource. Reading does not advance the aggregate serial.
func (r *Repository) LoadStateAndActions(ctx context.Context, topology string) (*state.State, []runtime.ActionRecord, error) {
	var st *state.State
	var actions []runtime.ActionRecord
	err := r.update(ctx, topology, func(a *aggregate) error {
		var err error
		st, err = r.snapshotState(a)
		if err != nil {
			return err
		}
		actions, err = snapshotActions(a)
		return err
	})
	return st, actions, err
}

func (r *Repository) LoadActions(ctx context.Context, topology string) ([]runtime.ActionRecord, error) {
	var out []runtime.ActionRecord
	err := r.update(ctx, topology, func(a *aggregate) error {
		var err error
		out, err = snapshotActions(a)
		return err
	})
	return out, err
}
func validateAction(a runtime.ActionRecord) error {
	if a.ID == "" || a.RunID == "" || !topologyName.MatchString(a.Topology) || a.Resource.IsZero() || a.OwnerUID == "" || a.ResourceUID == "" || a.Generation < 1 || a.StateSerial < 0 || a.ExpectedSerial < a.StateSerial || a.Revision < 0 {
		return fmt.Errorf("invalid action identity")
	}
	switch a.Action {
	case model.PlanActionCreate, model.PlanActionAdopt, model.PlanActionRead, model.PlanActionDelete, model.PlanActionReset:
	default:
		return fmt.Errorf("invalid action %s", a.Action)
	}
	if a.Action == model.PlanActionReset && resetPhaseOrder(a.Phase) < 0 {
		return fmt.Errorf("invalid reset phase %q", a.Phase)
	}
	return nil
}
func sameIntent(a, b runtime.ActionRecord) bool {
	if !runtime.SameResetSSHIntent(a, b) {
		return false
	}
	if !a.RunQueuedAt.Equal(b.RunQueuedAt) || a.RunRevision != b.RunRevision {
		return false
	}
	return a.ID == b.ID && a.Topology == b.Topology && a.RunID == b.RunID && a.Resource.Equal(b.Resource) && a.Action == b.Action && a.OwnerUID == b.OwnerUID && a.ResourceUID == b.ResourceUID && a.Generation == b.Generation && a.StateSerial == b.StateSerial && a.DesiredDigest == b.DesiredDigest && a.ReplayForbidden == b.ReplayForbidden
}
func sameRecord(a, b runtime.ActionRecord) bool {
	// An acknowledgement can be lost after commit. Identical terminal outcomes
	// remain idempotent even when the caller still has the pre-commit revision.
	a.Revision, b.Revision = 0, 0
	aRaw, _ := json.Marshal(a)
	bRaw, _ := json.Marshal(b)
	return string(aRaw) == string(bRaw)
}
func (r *Repository) CommitActionIntent(ctx context.Context, action runtime.ActionRecord) error {
	if err := validateAction(action); err != nil {
		return err
	}
	if action.DispositionID != "" || action.Status != runtime.ActionIntentCommitted || action.Revision != 0 {
		return fmt.Errorf("new action must be intent_committed")
	}
	if action.Action == model.PlanActionReset && (action.Phase != "accepted" || action.EffectComplete) {
		return fmt.Errorf("%w: reset intent must begin accepted without effects", ErrConflict)
	}
	return r.update(ctx, action.Topology, func(a *aggregate) error {
		return commitActionIntent(a, action)
	})
}

func commitActionIntent(a *aggregate, action runtime.ActionRecord) error {
	if gcPending(a) {
		return ErrConflict
	}
	if old, exists := a.Actions[action.ID]; exists {
		if old.DispositionID != "" {
			return ErrConflict
		}
		if sameIntent(old, action) {
			return nil
		}
		return ErrConflict
	}
	if _, exists := a.ResetContinuations[action.RunID]; exists {
		return ErrConflict
	}
	if a.State.TopologyUID != action.OwnerUID || a.Serial != action.ExpectedSerial || action.ExpectedSerial != action.StateSerial {
		return ErrConflict
	}
	for _, old := range a.Actions {
		if old.RunID == action.RunID && old.DispositionID != "" {
			return ErrConflict
		}
		if old.ID != action.ID && !runtime.ActionSettled(old) && (old.RunID != action.RunID || old.Resource.Equal(action.Resource)) {
			return fmt.Errorf("%w: unresolved action %s", ErrConflict, old.ID)
		}
	}
	if err := validateActionTarget(a.State, action); err != nil {
		return err
	}
	action.Revision = 1
	a.Actions[action.ID] = action
	a.UpdatedAt = time.Now().UTC()
	return nil
}
func (r *Repository) CommitActionOutcome(ctx context.Context, action runtime.ActionRecord, patch *runtime.StatePatch) error {
	if err := validateAction(action); err != nil {
		return err
	}
	return r.update(ctx, action.Topology, func(a *aggregate) error { return applyActionOutcome(a, action, patch) })
}

// CommitActionOutcomeWithGuard runs a caller-owned authorization snapshot check
// after acquiring the aggregate lock. The caller must hold the source record
// locks until this method returns; a preflight HTTP check is not such a lock.
func (r *Repository) CommitActionOutcomeWithGuard(ctx context.Context, action runtime.ActionRecord, patch *runtime.StatePatch, check func() error) error {
	if err := validateAction(action); err != nil {
		return err
	}
	if check == nil {
		return ErrConflict
	}
	return r.update(ctx, action.Topology, func(a *aggregate) error {
		if err := check(); err != nil {
			return err
		}
		return applyActionOutcome(a, action, patch)
	})
}

func applyActionOutcome(a *aggregate, action runtime.ActionRecord, patch *runtime.StatePatch) error {
	if gcPending(a) {
		return ErrConflict
	}
	old, exists := a.Actions[action.ID]
	if !exists {
		return ErrNotFound
	}
	if !sameIntent(old, action) {
		return ErrConflict
	}
	if old.DispositionID != "" || action.DispositionID != "" {
		return ErrConflict
	}
	if old.Status == runtime.ActionSucceeded {
		if sameRecord(old, action) {
			return nil
		}
		return ErrConflict
	}
	if a.Serial != action.ExpectedSerial || old.Revision != action.Revision {
		return ErrConflict
	}
	if err := validateActionTarget(a.State, action); err != nil {
		return err
	}
	if old.EffectComplete && !action.EffectComplete || old.Candidate != nil && action.Candidate == nil {
		return fmt.Errorf("%w: recovery evidence cannot be discarded", ErrConflict)
	}
	for i, before := range old.ResetSSH {
		after := action.ResetSSH[i]
		if before.Started && !after.Started {
			return ErrConflict
		}
		if before.Result != nil {
			x, _ := json.Marshal(before.Result)
			y, _ := json.Marshal(after.Result)
			if string(x) != string(y) {
				return ErrConflict
			}
		}
	}
	if old.Replacement != nil && (action.Replacement == nil || old.Replacement.ExternalID != action.Replacement.ExternalID || old.Replacement.Driver != action.Replacement.Driver) {
		return fmt.Errorf("%w: reset replacement cannot be discarded or substituted", ErrConflict)
	}
	if action.Action == model.PlanActionReset {
		before, after := resetPhaseOrder(old.Phase), resetPhaseOrder(action.Phase)
		if before < 1 || after < before || after > before+1 {
			return fmt.Errorf("%w: reset phase must advance one step or remain unchanged", ErrConflict)
		}
		if action.EffectComplete != (action.Phase == "verifying") || action.Status == runtime.ActionSucceeded && !action.EffectComplete {
			return fmt.Errorf("%w: reset completion requires verifying phase evidence", ErrConflict)
		}
	}
	switch action.Status {
	case runtime.ActionSucceeded, runtime.ActionUncertain, runtime.ActionBlocked:
	default:
		return fmt.Errorf("invalid outcome %s", action.Status)
	}
	if patch != nil {
		if action.Status != runtime.ActionSucceeded || patch.Resource != action.Resource.String() || patch.Action != action.Action {
			return ErrConflict
		}
		if patch.Op == runtime.StatePatchDelete {
			if action.Action != model.PlanActionDelete {
				return ErrConflict
			}
			a.State.RemoveResource(action.Resource)
		} else if patch.Op == runtime.StatePatchUpsert {
			if action.Candidate == nil || !action.Candidate.Address.Equal(action.Resource) {
				return ErrConflict
			}
			candidate := *action.Candidate
			candidate.OwnerUID = action.OwnerUID
			candidate.ResourceUID = action.ResourceUID
			candidate.Generation = action.Generation
			a.State.RemoveResource(action.Resource)
			a.State.AddResource(candidate)
			for _, dependency := range action.ResetSSH {
				if dependency.Result == nil {
					return ErrConflict
				}
				a.State.RemoveResource(dependency.Result.Address)
				a.State.AddResource(*dependency.Result)
			}
		} else {
			return fmt.Errorf("unsupported resource patch %s", patch.Op)
		}
		a.Serial++
	} else if action.Status == runtime.ActionSucceeded {
		return fmt.Errorf("verified success requires an atomic resource patch")
	}
	action.Revision++
	a.Actions[action.ID] = action
	if patch != nil {
		resolveIntentRecoveryRequirements(a, action)
	}
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// CommitAdoptionEvidenceAtomic records an authenticated host adoption result
// and its resource patch in one aggregate transaction. This closes the gap
// between separate intent/outcome calls for API retries and concurrent agents.
func (r *Repository) CommitAdoptionEvidenceAtomic(ctx context.Context, runID, topology string, addr address.Address, evidence substrate.AdoptionEvidence, resource state.Resource, identity substrate.OperationIdentity, expectedSerial int64) error {
	if runID == "" || !topologyName.MatchString(topology) || addr.IsZero() || identity.Validate() != nil || evidence.ExternalID == "" || evidence.Fingerprint == "" || !resource.Address.Equal(addr) || resource.Driver != evidence.Provider || resource.ExternalID != evidence.ExternalID || resource.OwnerUID != identity.OwnerUID || resource.ResourceUID != identity.ResourceUID || resource.Generation != identity.Generation || identity.TopologyUID != identity.OwnerUID {
		return ErrConflict
	}
	stable := resource
	stable.CreatedAt, stable.UpdatedAt = time.Time{}, time.Time{}
	raw, err := json.Marshal(struct {
		Run, Topology string
		Identity      substrate.OperationIdentity
		Evidence      substrate.AdoptionEvidence
		Resource      state.Resource
	}{runID, topology, identity, evidence, stable})
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	action := runtime.ActionRecord{ID: "adopt-" + digest, Topology: topology, RunID: runID, Resource: addr, Action: model.PlanActionAdopt, Status: runtime.ActionIntentCommitted, DesiredDigest: digest, OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation, StateSerial: expectedSerial, ExpectedSerial: expectedSerial, Candidate: &resource}
	if err := validateAction(action); err != nil {
		return err
	}
	return r.update(ctx, topology, func(a *aggregate) error {
		return commitAdoptionEvidence(a, action, addr, identity, expectedSerial, digest)
	})
}
func commitAdoptionEvidence(a *aggregate, action runtime.ActionRecord, addr address.Address, identity substrate.OperationIdentity, expectedSerial int64, digest string) error {

	if old, ok := a.Actions[action.ID]; ok {
		if old.DispositionID != "" {
			return ErrConflict
		}
		if old.Candidate == nil || old.DesiredDigest != digest || old.ID != action.ID || old.Topology != action.Topology || old.RunID != action.RunID || !old.Resource.Equal(action.Resource) || old.Action != action.Action || old.OwnerUID != action.OwnerUID || old.ResourceUID != action.ResourceUID || old.Generation != action.Generation || old.StateSerial != action.StateSerial || !old.RunQueuedAt.Equal(action.RunQueuedAt) || old.RunRevision != action.RunRevision {
			return ErrConflict
		}
		if old.Status == runtime.ActionSucceeded {
			oldCandidate, newCandidate := *old.Candidate, *action.Candidate
			oldCandidate.CreatedAt, oldCandidate.UpdatedAt = time.Time{}, time.Time{}
			newCandidate.CreatedAt, newCandidate.UpdatedAt = time.Time{}, time.Time{}
			oldRaw, _ := json.Marshal(oldCandidate)
			newRaw, _ := json.Marshal(newCandidate)
			if string(oldRaw) != string(newRaw) {
				return ErrConflict
			}
			return nil
		}
		// A durable intent is the authority after an interrupted host
		// response. Compare its canonical candidate, while retaining the
		// original digest (timestamps are intentionally non-authoritative).
		action = old
	}
	if a.State.TopologyUID != identity.TopologyUID || a.Serial != expectedSerial {
		return fmt.Errorf("%w: adoption serial/topology mismatch got serial=%d want=%d", runtime.ErrPlanStale, a.Serial, expectedSerial)
	}
	if a.State.FindResource(addr) != nil {
		return fmt.Errorf("%w: adoption resource already present", runtime.ErrPlanStale)
	}
	for _, old := range a.Actions {
		if old.RunID == action.RunID && old.DispositionID != "" {
			return ErrConflict
		}
		if old.ID != action.ID && !runtime.ActionSettled(old) && (old.RunID != action.RunID || old.Resource.Equal(action.Resource)) {
			return ErrConflict
		}
	}
	action.Revision = 1
	a.Actions[action.ID] = action
	candidate := *action.Candidate
	candidate.OwnerUID, candidate.ResourceUID, candidate.Generation = action.OwnerUID, action.ResourceUID, action.Generation
	a.State.RemoveResource(addr)
	a.State.AddResource(candidate)
	a.Serial++
	action.Status = runtime.ActionSucceeded
	action.EffectComplete = true
	action.Revision++
	a.Actions[action.ID] = action
	a.UpdatedAt = time.Now().UTC()
	return nil

}

func resetPhaseOrder(phase string) int {
	switch phase {
	case "":
		return 0
	case "accepted":
		return 1
	case "preparing":
		return 2
	case "prepared":
		return 3
	case "destroyed":
		return 6
	case "destroy_returned":
		return 5
	case "destroying":
		return 4
	case "applying":
		return 7
	case "verifying":
		return 8
	default:
		return -1
	}
}

// Serial is necessary but not sufficient after external restore/import. An
// action may only update the resource identity and generation it authorized.
func validateActionTarget(st *state.State, action runtime.ActionRecord) error {
	if err := action.ValidateResetSSH(st); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	current := st.FindResource(action.Resource)
	if current == nil {
		if action.Action == model.PlanActionDelete || action.Action == model.PlanActionReset {
			return ErrConflict
		}
		return nil
	}
	if action.Action == model.PlanActionCreate || action.Action == model.PlanActionAdopt {
		return ErrConflict
	}
	want := action.Generation
	if action.Action == model.PlanActionReset {
		want--
	}
	if current.OwnerUID != action.OwnerUID || current.ResourceUID != action.ResourceUID || current.Generation != want {
		return ErrConflict
	}
	if action.Action == model.PlanActionDelete && (action.Candidate == nil || current.ExternalID != action.Candidate.ExternalID) {
		return ErrConflict
	}
	return nil
}

// StateBackend exposes the same aggregate to existing state.Manager callers.
// It does not create a second mutable state file beside the action journal.
func (r *Repository) StateBackend(topology string) state.Backend {
	return &stateBackend{repository: r, topology: topology}
}

type stateBackend struct {
	repository *Repository
	topology   string
}

func (b *stateBackend) Capabilities() state.BackendCapabilities {
	return state.BackendCapabilities{Locking: true, CAS: true, Delete: true, Snapshot: true}
}
func (b *stateBackend) Metadata(ctx context.Context) (state.Metadata, error) {
	st, err := b.repository.LoadState(ctx, b.topology)
	if err != nil {
		return state.Metadata{}, err
	}
	return state.Metadata{Backend: b.repository.backend.name(), Location: b.topology, Version: state.SchemaVersion, Serial: st.Meta.Serial, UpdatedAt: st.Meta.UpdatedAt}, nil
}
func (b *stateBackend) ListTopologies(ctx context.Context) ([]state.TopologyMetadata, error) {
	return b.repository.ListTopologies(ctx)
}
func (b *stateBackend) Delete(ctx context.Context) error {
	return b.repository.update(ctx, b.topology, resetAggregate)
}
func (b *stateBackend) DeleteVersioned(ctx context.Context, expectedSerial int64, topologyUID string) error {
	return b.repository.update(ctx, b.topology, func(a *aggregate) error {
		if a.Serial != expectedSerial || a.State.TopologyUID != topologyUID {
			return ErrConflict
		}
		return resetAggregate(a)
	})
}
func resetAggregate(a *aggregate) error {
	if ownershipPending(a) {
		return ErrConflict
	}
	if len(a.GCSources) > 0 {
		return ErrConflict
	}
	if gcPending(a) {
		return ErrConflict
	}
	for _, action := range a.Actions {
		if !runtime.ActionSettled(action) {
			return ErrConflict
		}
	}
	a.DeletedFrom = nil
	a.State = &state.State{Version: state.SchemaVersion, TopologyUID: uuid.NewString()}
	a.Actions = map[string]runtime.ActionRecord{}
	a.Snapshots = map[string]aggregateSnapshot{}
	a.Restores = map[string]SnapshotRestoreReceipt{}
	a.ResetContinuations = map[string]runtime.ResetContinuationReceipt{}
	a.GC = map[string]GCReservation{}
	a.Serial = 0
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// Each conditional save below locks and checks within one backend transaction.
func (b *stateBackend) Lock(ctx context.Context) (state.UnlockFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
func (b *stateBackend) Load(ctx context.Context) ([]byte, error) {
	st, err := b.repository.LoadState(ctx, b.topology)
	if err != nil {
		return nil, err
	}
	return st.Marshal()
}
func (b *stateBackend) LoadVersioned(ctx context.Context) (*state.LoadedState, error) {
	st, err := b.repository.LoadState(ctx, b.topology)
	if err != nil {
		return nil, err
	}
	raw, err := st.Marshal()
	return &state.LoadedState{Data: raw, Serial: st.Meta.Serial, Exists: true, UpdatedAt: st.Meta.UpdatedAt, Metadata: state.Metadata{Backend: st.Meta.Backend, Serial: st.Meta.Serial, Version: state.SchemaVersion}}, err
}
func (b *stateBackend) Save(ctx context.Context, raw []byte) error {
	return fmt.Errorf("unconditional aggregate save is unsupported; use CAS")
}
func (b *stateBackend) SaveVersioned(ctx context.Context, raw []byte, options state.SaveOptions) error {
	st, err := state.Unmarshal(raw)
	if err != nil {
		return err
	}
	return b.repository.update(ctx, b.topology, func(a *aggregate) error {
		if !options.RequireCAS || a.Serial != options.ExpectedSerial {
			return &state.ConflictError{Expected: options.ExpectedSerial, Actual: a.Serial}
		}
		if st.TopologyUID == "" && a.Serial == 0 && len(a.Actions) == 0 {
			st.TopologyUID = a.State.TopologyUID
		}
		if st.TopologyUID != a.State.TopologyUID {
			return ErrConflict
		}
		for _, action := range a.Actions {
			if !runtime.ActionSettled(action) {
				return fmt.Errorf("%w: unfinished action prevents standalone state save", ErrConflict)
			}
		}
		a.State = st
		a.Serial++
		a.UpdatedAt = time.Now().UTC()
		return nil
	})
}

var _ runtime.ActionRepository = (*Repository)(nil)
