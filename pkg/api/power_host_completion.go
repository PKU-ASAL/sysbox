package api

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) completePowerFromHost(ctx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
	if err := controlstore.ValidateHostOperationReceipt(ctx, s.apiStore, receipt); err != nil {
		return err
	}
	store, ok := s.apiStore.(interface {
		TransitionNodeOperation(context.Context, string, string, *controlplane.SignedExecutionStopReceipt, func(controlplane.NodeOperation, *persistence.CommandRecord) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error)
	})
	if !ok {
		return persistence.ErrUnsupportedVersion
	}
	g := receipt.Grant
	_, err := store.TransitionNodeOperation(ctx, g.AgentID, g.ActionID, &receipt, func(current controlplane.NodeOperation, record *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompletePowerFromHost(current, record, g.AgentID, g.ActionID, receipt)
	})
	if err != nil {
		return err
	}
	// Retiring after durable host completion is retryable with the same receipt.
	return s.apiStore.DeleteAgentCommand(ctx, g.AgentID, g.CommandID)
}
