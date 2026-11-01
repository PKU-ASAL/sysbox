package api

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) reserveGCWithAuthority(ctx context.Context, topology string, request persistence.GCReservation) error {
	return s.storeTransactions().ReserveGCWithAuthority(ctx, topology, request)
}

func (s *Server) reserveGCBatchWithAuthority(ctx context.Context, topology string, requests []persistence.GCReservation) error {
	return s.storeTransactions().ReserveGCBatchWithAuthority(ctx, topology, requests)
}

func (s *Server) beginOwnershipMaintenance(ctx context.Context, p controlplane.OwnershipTransferPlan) (*persistence.MaintenanceBarrier, error) {
	return s.storeTransactions().BeginOwnershipMaintenance(ctx, p)
}

func (s *Server) abortOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	return s.storeTransactions().AbortOwnershipTransfer(ctx, p)
}

func (s *Server) releaseHostFence(ctx context.Context, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	return s.storeTransactions().ReleaseHostFence(ctx, fence, request)
}

func (s *Server) reconcileFenceIntent(ctx context.Context, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest, decision controlplane.IntentDispositionDecision) (persistence.FenceIntentReconciliation, error) {
	return s.storeTransactions().ReconcileFenceIntent(ctx, fence, request, decision)
}

func (s *Server) revisionRunReferences(ctx context.Context) (map[string]bool, error) {
	return s.storeTransactions().RevisionRunReferences(ctx)
}

func (s *Server) globalRevisionInventory(ctx context.Context) ([]controlplane.GlobalRevision, error) {
	return s.storeTransactions().GlobalRevisionInventory(ctx)
}

func (s *Server) globalRevisionInventoryWithFileLock(ctx context.Context, lock bool) ([]controlplane.GlobalRevision, error) {
	return s.storeTransactions().GlobalRevisionInventoryWithFileLock(ctx, lock)
}

func (s *Server) lifecycleRevisionReferences(ctx context.Context) (map[string]bool, error) {
	return s.storeTransactions().LifecycleRevisionReferences(ctx)
}
