package workspace

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

// currentCleanupReceipt identifies bounded, persisted evidence for the current
// placement. The caller must still inspect the local aggregate and serialize
// deletion against admission. This never constitutes live global absence.
func CurrentCleanupReceipt(topology string, placement *controlstore.Placement, runs []controlplane.Run, operations []controlplane.NodeOperation) (*controlplane.CleanupReceipt, string) {
	if placement == nil {
		return nil, "no_owning_agent_placement"
	}
	latestRuns := controlstore.LatestRunsByID(runs)
	var latest *controlplane.Run
	for _, run := range latestRuns {
		if run.Topology != topology {
			continue
		}
		if run.Status.IsActive() {
			return nil, "active_run"
		}
		if run.QueuedAt.IsZero() {
			return nil, "run_order_unknown"
		}
		if latest == nil || run.QueuedAt.After(latest.QueuedAt) {
			copy := run
			latest = &copy
		}
	}
	if latest == nil {
		return nil, "cleanup_receipt_missing"
	}
	for _, run := range latestRuns {
		if run.Topology == topology && run.ID != latest.ID && run.QueuedAt.Equal(latest.QueuedAt) {
			return nil, "ambiguous_run_order"
		}
	}
	if latest.Op != "destroy" || latest.Status != controlplane.RunDone {
		return nil, "latest_run_not_successful_destroy"
	}
	if latest.Cleanup == nil {
		return nil, "cleanup_receipt_missing"
	}
	if persistence.ValidateCleanupReceipt(*latest, latest.Cleanup) != nil {
		return nil, "cleanup_receipt_invalid"
	}
	if latest.AgentID != placement.AgentID || placement.BoundAt.IsZero() || latest.StartedAt.Before(placement.BoundAt) || latest.Cleanup.ObservedAt.Before(placement.BoundAt) {
		return nil, "placement_changed"
	}
	for _, op := range operations {
		if op.Topology != topology {
			continue
		}
		if op.Status != controlplane.NodeOperationStatusDone && op.Status != controlplane.NodeOperationStatusFailed {
			return nil, "active_node_operation"
		}
		if op.CreatedAt.IsZero() || op.EndedAt.IsZero() {
			return nil, "node_operation_order_unknown"
		}
		if !op.CreatedAt.Before(latest.QueuedAt) || !op.StartedAt.IsZero() && !op.StartedAt.Before(latest.QueuedAt) || !op.EndedAt.Before(latest.QueuedAt) {
			return nil, "node_operation_after_destroy_began"
		}
	}
	copy := *latest.Cleanup
	return &copy, "current_recorded"
}
