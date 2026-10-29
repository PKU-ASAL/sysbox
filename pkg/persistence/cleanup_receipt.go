package persistence

import (
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// ValidateCleanupReceipt checks the complete binding of a successful whole-
// topology destroy. Placement and subsequent-admission invalidation belong to
// the caller; a receipt alone never establishes current deletion eligibility.
func ValidateCleanupReceipt(run controlplane.Run, receipt *controlplane.CleanupReceipt) error {
	controlplane.NormalizeRun(&run)
	if receipt == nil || receipt.Version != 1 || !receipt.Empty || strings.TrimSpace(receipt.TopologyUID) == "" || receipt.StateSerial <= 0 {
		return ErrConflict
	}
	if run.Operation != "destroy" || run.Op != "destroy" || run.Status != controlplane.RunDone || run.Err != "" || run.Target != "" {
		return ErrConflict
	}
	if run.ID == "" || run.Topology == "" || run.AgentID == "" || run.Attempt < 1 || run.LeaseOwner == "" || run.QueuedAt.IsZero() || run.StartedAt.IsZero() {
		return ErrConflict
	}
	if receipt.RunID != run.ID || receipt.Topology != run.Topology || receipt.AgentID != run.AgentID || receipt.Attempt != run.Attempt || receipt.LeaseOwner != run.LeaseOwner || receipt.Revision != run.Revision || !receipt.QueuedAt.Equal(run.QueuedAt) || receipt.ObservedAt.IsZero() || receipt.ObservedAt.Before(run.StartedAt) {
		return ErrConflict
	}
	return nil
}

func sameCleanupReceipt(a, b *controlplane.CleanupReceipt) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Version == b.Version && a.Topology == b.Topology && a.TopologyUID == b.TopologyUID && a.AgentID == b.AgentID && a.RunID == b.RunID && a.QueuedAt.Equal(b.QueuedAt) && a.Revision == b.Revision && a.Attempt == b.Attempt && a.LeaseOwner == b.LeaseOwner && a.StateSerial == b.StateSerial && a.ObservedAt.Equal(b.ObservedAt) && a.Empty == b.Empty
}
