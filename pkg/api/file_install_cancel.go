package api

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Retire dispatch and revoke an existing installation under the same command /
// authority lock used by issuance. Only absence of any grant proves no effect
// could have started and permits immediate queue cancellation.
func (s *Server) cancelFileDispatch(ctx context.Context, op controlplane.GuestFileOperation) (bool, error) {
	if op.CommandID == "" {
		return false, persistence.ErrConflict
	}
	noEffect := true
	storage := controlstore.AuthorityStorage{Store: s.apiStore, CommandID: op.CommandID, WriteCommand: true, Validate: func(record *persistence.CommandRecord) error {
		if record == nil || record.Command.AgentID != op.AgentID || record.Command.Type != "guest_file_put" || record.Command.FilePut == nil || record.Command.FilePut.ID != op.ID {
			return persistence.ErrConflict
		}
		record.Retired = true
		return nil
	}}
	err := storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		memory := &controlstore.GrantTransaction{Raw: raw}
		repo := persistence.NewExecutionGrantRepository(memory)
		grants, err := repo.ListActiveGrants(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range grants {
			if g.CommandID == op.CommandID && g.AgentID == op.AgentID {
				noEffect = false
				if err = repo.Revoke(ctx, g); err != nil {
					return nil, err
				}
			}
		}
		if memory.Raw == nil {
			if err = repo.ObserveTime(ctx); err != nil {
				return nil, err
			}
		}
		return memory.Raw, nil
	})
	return noEffect, err
}
