package api

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
)

type runCommandStorage interface {
	updateRunWithCommand(context.Context, string, string, func(*controlplane.Run, *persistence.CommandRecord) (*controlplane.Run, error)) (*controlplane.Run, error)
}

func (s *localAPIStore) CompleteRunFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt, observed controlplane.Run) (*controlplane.Run, error) {
	return completeRunFromHost(ctx, localRunStorage{store: s}, agent, id, receipt, observed)
}
func (s *sqliteAPIStore) CompleteRunFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt, observed controlplane.Run) (*controlplane.Run, error) {
	return completeRunFromHost(ctx, sqliteRunStorage{store: s}, agent, id, receipt, observed)
}
func (s *postgresAPIStore) CompleteRunFromHost(ctx context.Context, agent, id string, receipt controlplane.SignedExecutionStopReceipt, observed controlplane.Run) (*controlplane.Run, error) {
	return completeRunFromHost(ctx, postgresRunStorage{store: s}, agent, id, receipt, observed)
}

// Only the authenticated host route invokes this transition. The original
// command and current Run are read under one lock; no worker lease is renewed
// or substituted to admit historical evidence.
func completeRunFromHost(ctx context.Context, storage runCommandStorage, agent, id string, receipt controlplane.SignedExecutionStopReceipt, observed controlplane.Run) (*controlplane.Run, error) {
	g := receipt.Grant
	return storage.updateRunWithCommand(ctx, id, g.CommandID, func(current *controlplane.Run, record *persistence.CommandRecord) (*controlplane.Run, error) {
		if current == nil || record == nil {
			return nil, persistence.ErrNotFound
		}
		cmd := record.Command
		// Dispatch may have been re-leased since this grant was issued. Its
		// durable authority epoch authenticates that historical attempt; the
		// actual Run attempt and owner below must still match the signed result.
		if cmd.Run == nil || cmd.Type != "run_assigned" || cmd.ID != g.CommandID || cmd.AgentID != agent || g.AgentID != agent || cmd.Attempt < 1 || uint64(cmd.Attempt) < g.Attempt || cmd.ActionID != g.ActionID || cmd.TopologyUID != g.Scope.TopologyUID || cmd.ResourceUID != g.Scope.ResourceUID || cmd.Generation != g.Scope.Generation || receipt.EvidenceDigest == "" {
			return nil, persistence.ErrConflict
		}
		if current.ID != id || current.AgentID != agent || !persistence.SameRunIncarnation(*current, *cmd.Run) || !persistence.SameRunIncarnation(*current, observed) || current.Attempt < 1 || current.Attempt != observed.Attempt || current.LeaseOwner != observed.LeaseOwner || !observed.Status.IsTerminal() || observed.EndedAt.IsZero() {
			return nil, persistence.ErrConflict
		}
		req := hostsupervisor.RunRequest{RunID: cmd.Run.ID, Topology: cmd.Run.Topology, TopologyUID: cmd.TopologyUID, Revision: cmd.Run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: current.Operation, Target: cmd.Run.Target, ExpectedSerial: cmd.ExpectedSerial}
		digest, err := req.Digest()
		if err != nil || digest != g.Digest || g.Operation != "run_"+req.Action {
			return nil, persistence.ErrConflict
		}
		return persistence.CompleteRunFromHost(*current, observed, time.Now().UTC())
	})
}
