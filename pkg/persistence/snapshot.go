package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type aggregateSnapshot struct {
	Metadata    state.Snapshot `json:"metadata"`
	TopologyUID string         `json:"topology_uid"`
	Serial      int64          `json:"serial"`
	Digest      string         `json:"digest"`
	// Preserve exact hashed bytes across JSONB normalization in PostgreSQL.
	Data []byte `json:"data"`
}

// Capture excludes the snapshot catalog itself, avoiding recursive snapshots.
// The resource state and action journal remain one indivisible transaction.
func (b *stateBackend) Snapshot(ctx context.Context, reason string) (*state.Snapshot, error) {
	var result state.Snapshot
	err := b.repository.update(ctx, b.topology, func(a *aggregate) error {
		content := *a
		content.Snapshots = map[string]aggregateSnapshot{}
		content.Restores = map[string]SnapshotRestoreReceipt{}
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		result = state.Snapshot{ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Reason: reason, Size: int64(len(raw))}
		a.Snapshots[result.ID] = aggregateSnapshot{Metadata: result, TopologyUID: a.State.TopologyUID, Serial: a.Serial, Digest: fmt.Sprintf("%x", sha256.Sum256(raw)), Data: raw}
		return nil
	})
	return &result, err
}
func (b *stateBackend) ListSnapshots(ctx context.Context) ([]state.Snapshot, error) {
	out := []state.Snapshot{}
	err := b.repository.update(ctx, b.topology, func(a *aggregate) error {
		for id, snap := range a.Snapshots {
			if _, err := decodeSnapshot(id, snap); err != nil {
				return err
			}
			out = append(out, snap.Metadata)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, err
}

// SnapshotResources returns durable resource identities retained by every
// valid snapshot. GC uses this reference set so a resource removed from the
// current state is not deleted while a recovery snapshot still names it.
func (r *Repository) SnapshotResources(ctx context.Context, topology string) ([]state.Resource, error) {
	var out []state.Resource
	err := r.update(ctx, topology, func(a *aggregate) error {
		for id, snap := range a.Snapshots {
			restored, err := decodeSnapshot(id, snap)
			if err != nil {
				return err
			}
			out = append(out, restored.State.Resources...)
		}
		return nil
	})
	return out, err
}
func decodeSnapshot(id string, snap aggregateSnapshot) (*aggregate, error) {
	if id == "" || id != snap.Metadata.ID || snap.Metadata.CreatedAt.IsZero() || snap.Metadata.Size != int64(len(snap.Data)) || snap.Digest != fmt.Sprintf("%x", sha256.Sum256(snap.Data)) {
		return nil, ErrCorruptRecord
	}
	restored, err := decodeAggregate(snap.Data)
	if err != nil {
		return nil, err
	}
	if restored.State.TopologyUID != snap.TopologyUID || restored.Serial != snap.Serial || len(restored.Snapshots) != 0 || len(restored.Restores) != 0 {
		return nil, ErrCorruptRecord
	}
	return restored, nil
}

// Metadata restoration never rolls back an external resource's incarnation or
// replaces current journal authority. A different live inventory needs an
// observed, explicitly authorized provider restore/adoption plan first.
func (b *stateBackend) RestoreSnapshot(ctx context.Context, id string) error {
	return b.repository.update(ctx, b.topology, func(a *aggregate) error { return restoreSnapshot(a, id) })
}

func restoreSnapshot(a *aggregate, id string) error {
	restored, err := prepareSnapshotRestore(a, id)
	if err != nil {
		return err
	}
	a.State = restored
	a.Serial++
	a.UpdatedAt = time.Now().UTC()
	return nil
}

func prepareSnapshotRestore(a *aggregate, id string) (*state.State, error) {
	snap, ok := a.Snapshots[id]
	if !ok {
		return nil, ErrNotFound
	}
	restored, err := decodeSnapshot(id, snap)
	if err != nil {
		return nil, err
	}
	if restored.State.TopologyUID != a.State.TopologyUID || restored.State.Lineage != a.State.Lineage || restored.Serial > a.Serial {
		return nil, ErrConflict
	}
	// An unresolved intent may be retained, never introduced, discarded,
	// rewritten or resolved by metadata restoration. Compare both directions;
	// keep the current journal (including all newer completed history).
	for _, pair := range [][2]map[string]runtime.ActionRecord{{a.Actions, restored.Actions}, {restored.Actions, a.Actions}} {
		for id, action := range pair[0] {
			if action.Status == runtime.ActionSucceeded {
				continue
			}
			other, ok := pair[1][id]
			if !ok || !reflect.DeepEqual(action, other) {
				return nil, fmt.Errorf("%w: unresolved journal differs from snapshot", ErrConflict)
			}
		}
	}
	if len(restored.State.Resources) != len(a.State.Resources) {
		return nil, fmt.Errorf("%w: snapshot inventory needs provider reconciliation", ErrConflict)
	}
	for i := range restored.State.Resources {
		resource := &restored.State.Resources[i]
		current := a.State.FindResource(resource.Address)
		if current == nil || current.OwnerUID != resource.OwnerUID || current.ResourceUID != resource.ResourceUID || current.Generation != resource.Generation || current.ExternalID != resource.ExternalID || current.Driver != resource.Driver {
			return nil, fmt.Errorf("%w: snapshot resource incarnation changed", ErrConflict)
		}
		if current.ResourceType != resource.ResourceType || current.SchemaVersion != resource.SchemaVersion {
			return nil, fmt.Errorf("%w: snapshot resource schema changed", ErrConflict)
		}
		if !reflect.DeepEqual(current.Attributes, resource.Attributes) || !reflect.DeepEqual(current.Dependencies, resource.Dependencies) {
			return nil, fmt.Errorf("%w: changed resource attributes require an observed restore plan", ErrConflict)
		}
		// Never restore historical process handles, attachment proofs or secrets.
		resource.Private = current.Private
		resource.Attachments = current.Attachments
		resource.Status = state.ResourceUnknown
	}
	return restored.State, nil
}

func (b *stateBackend) DeleteSnapshot(ctx context.Context, id string) error {
	return b.repository.update(ctx, b.topology, func(a *aggregate) error {
		if _, ok := a.Snapshots[id]; !ok {
			return ErrNotFound
		}
		delete(a.Snapshots, id)
		return nil
	})
}
