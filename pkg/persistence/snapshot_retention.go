package persistence

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/runtime"
)

type SnapshotRetentionPolicy struct {
	KeepLast  int       `json:"keep_last"`
	OlderThan time.Time `json:"older_than"`
	Limit     int       `json:"limit"`
}
type SnapshotRetentionPlan struct {
	Version       int                     `json:"version"`
	Topology      string                  `json:"topology"`
	TopologyUID   string                  `json:"topology_uid"`
	CatalogSHA256 string                  `json:"catalog_sha256"`
	Policy        SnapshotRetentionPolicy `json:"policy"`
	DeleteIDs     []string                `json:"delete_ids"`
	Protected     map[string]string       `json:"protected"`
}

// The full aggregate hash binds live references, snapshot contents and restore
// receipts. Snapshot deletion only removes metadata references, never resources.
func planSnapshotRetention(a *aggregate, topology string, policy SnapshotRetentionPolicy, now time.Time) (SnapshotRetentionPlan, error) {
	var zero SnapshotRetentionPlan
	if policy.KeepLast < 1 || policy.Limit < 1 || policy.Limit > 128 || policy.OlderThan.IsZero() || policy.OlderThan.After(now) {
		return zero, ErrInvalidRecord
	}
	if gcPending(a) {
		return zero, ErrConflict
	}
	plan := SnapshotRetentionPlan{Version: 1, Topology: topology, TopologyUID: a.State.TopologyUID, CatalogSHA256: evidenceHash(a), Policy: policy, DeleteIDs: []string{}, Protected: map[string]string{}}
	ordered := make([]aggregateSnapshot, 0, len(a.Snapshots))
	protected := map[string]string{}
	for id, snapshot := range a.Snapshots {
		content, err := decodeSnapshot(id, snapshot)
		if err != nil {
			return zero, err
		}
		if err = validateAggregate(content, topology); err != nil {
			return zero, err
		}
		reason := "eligible"
		for _, action := range content.Actions {
			if action.Status != runtime.ActionSucceeded {
				reason = "unresolved_journal"
				break
			}
		}
		if len(content.ResetContinuations) > 0 || gcPending(content) {
			reason = "unfinished_workflow"
		}
		if reason != "eligible" {
			protected[id] = reason
		}
		ordered = append(ordered, snapshot)
	}
	for _, restore := range a.Restores {
		if _, ok := a.Snapshots[restore.Request.SnapshotID]; ok {
			protected[restore.Request.SnapshotID] = "restore_receipt"
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].Metadata, ordered[j].Metadata
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	for i, snapshot := range ordered {
		if i < policy.KeepLast {
			protected[snapshot.Metadata.ID] = "keep_last"
		}
	}
	for i := len(ordered) - 1; i >= 0; i-- {
		snapshot := ordered[i].Metadata
		if _, ok := protected[snapshot.ID]; ok {
			continue
		}
		if !snapshot.CreatedAt.Before(policy.OlderThan) {
			protected[snapshot.ID] = "minimum_age"
			continue
		}
		if len(plan.DeleteIDs) < policy.Limit {
			plan.DeleteIDs = append(plan.DeleteIDs, snapshot.ID)
		} else {
			protected[snapshot.ID] = "batch_limit"
		}
	}
	// Public plan is bounded; caller can request the next page after applying this batch.
	if len(protected) > 256 {
		plan.Protected = map[string]string{"__summary__": fmt.Sprintf("protected=%d;catalog_sha256=%s", len(protected), plan.CatalogSHA256)}
	} else {
		plan.Protected = protected
	}
	return plan, nil
}

func (r *Repository) PlanSnapshotRetention(ctx context.Context, topology string, policy SnapshotRetentionPolicy) (SnapshotRetentionPlan, error) {
	var plan SnapshotRetentionPlan
	err := r.update(ctx, topology, func(a *aggregate) error {
		var err error
		plan, err = planSnapshotRetention(a, topology, policy, time.Now().UTC())
		return err
	})
	return plan, err
}
func (r *Repository) ApplySnapshotRetention(ctx context.Context, topology string, plan SnapshotRetentionPlan) ([]string, error) {
	var removed []string
	if plan.Version != 1 || plan.Topology != topology {
		return nil, ErrConflict
	}
	err := r.update(ctx, topology, func(a *aggregate) error {
		expected, err := planSnapshotRetention(a, topology, plan.Policy, time.Now().UTC())
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(expected, plan) {
			return fmt.Errorf("%w: snapshot catalog or retention plan changed", ErrConflict)
		}
		for _, id := range expected.DeleteIDs {
			delete(a.Snapshots, id)
		}
		removed = append([]string{}, expected.DeleteIDs...)
		return nil
	})
	return removed, err
}
