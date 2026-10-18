package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) completeGCFromHost(ctx context.Context, receipt controlplane.SignedExecutionStopReceipt, key ed25519.PublicKey) error {
	record, err := readFailureCommand(ctx, s.apiStore, receipt.Grant.CommandID)
	if err != nil {
		return err
	}
	if record == nil {
		return persistence.ErrNotFound
	}
	cmd := record.Command
	if cmd.Type != "gc_delete" || cmd.AgentID != receipt.Grant.AgentID || cmd.Session == nil || cmd.ActionID != receipt.Grant.ActionID {
		return persistence.ErrConflict
	}
	topology, err := grantCommandTopology(record)
	if err != nil {
		return err
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		return err
	}
	reservation, err := repo.LoadGCReservation(ctx, topology, cmd.ID)
	if err != nil {
		return err
	}
	if err := controlplane.VerifyGCDeletionReceipt(key, receipt, reservation.Candidate); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	// Retire command first: stopping authority while a command remains issuable
	// would permit another epoch before the reservation is released. Exact host
	// stop validation allows retry after retirement, including after a crash.
	if err := authority.ValidateHostStop(ctx, receipt.Grant, hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	var storage persistence.CommandStorage
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		storage = localCommandStorage{store: store}
	case *sqliteAPIStore:
		storage = sqliteCommandStorage{store: store}
	case *postgresAPIStore:
		storage = postgresCommandStorage{store: store}
	default:
		return persistence.ErrUnsupportedVersion
	}
	_, err = storage.UpdateCommand(ctx, cmd.ID, func(current *persistence.CommandRecord) (*persistence.CommandRecord, error) {
		if current == nil || current.Command.ID != cmd.ID || current.Command.AgentID != cmd.AgentID || current.Command.ActionID != cmd.ActionID || current.Command.TopologyUID != cmd.TopologyUID || current.Command.GCExternalID != cmd.GCExternalID || current.Command.GCInventoryDigest != cmd.GCInventoryDigest {
			return nil, persistence.ErrConflict
		}
		current.Command.Status = controlplane.AgentCommandStatusCompleted
		current.Command.Err = ""
		current.Command.EndedAt = receipt.StoppedAt
		current.Retired = true
		return current, nil
	})
	if err != nil {
		return err
	}
	if err := authority.AcceptHostStop(ctx, receipt.Grant, hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	if err := repo.CompleteGC(ctx, topology, receipt, key); err != nil {
		return err
	}
	return s.reconcileGCOutbox(ctx, topology)
}
