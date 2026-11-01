package api

import (
	"context"
	"crypto/ed25519"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) storeTransactions() *controlstore.Transactions {
	return &controlstore.Transactions{Store: s.apiStore, ActionRepository: s.actionRepository, GrantHostKeys: s.grantHostKeys, StateBackend: s.stateBackend, RunsDir: s.runsDir}
}

func (s *Server) commitSnapshotRestore(ctx context.Context, topology string, request persistence.SnapshotRestoreRequest) (persistence.SnapshotRestoreReceipt, error) {
	return s.storeTransactions().CommitSnapshotRestore(ctx, topology, request)
}

func (s *Server) commitOwnershipRetirement(ctx context.Context, topology string, request controlplane.OwnershipTransferPlan) (controlplane.OwnershipRetirementProof, error) {
	return s.storeTransactions().CommitOwnershipRetirement(ctx, topology, request)
}

func (s *Server) deleteStateWithAuthority(ctx context.Context, topology string, serial int64, uid string) error {
	return s.storeTransactions().DeleteStateWithAuthority(ctx, topology, serial, uid)
}

func (s *Server) claimGCExecution(ctx context.Context, topology string, c controlplane.GCCandidate, g controlplane.ExecutionGrant) error {
	return s.storeTransactions().ClaimGCExecution(ctx, topology, c, g)
}

func (s *Server) commitAdoptionWithAuthority(ctx context.Context, topology string, apply func([]byte, []byte) ([]byte, error)) error {
	return s.storeTransactions().CommitAdoptionWithAuthority(ctx, topology, apply)
}

func (s *Server) commitFencedAdoption(ctx context.Context, topology string, p controlplane.AdoptionDispositionProof, host, issuer ed25519.PublicKey) error {
	return s.storeTransactions().CommitFencedAdoption(ctx, topology, p, host, issuer)
}

func (s *Server) reconcileFenceDelete(ctx context.Context, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.FenceDeleteReconciliation, error) {
	return s.storeTransactions().ReconcileFenceDelete(ctx, fence, request)
}
