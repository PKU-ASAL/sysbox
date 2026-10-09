package api

import (
	"errors"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Read the operation while holding execution authority, so cancellation and
// grant issue/renewal cannot both commit using an obsolete source snapshot.
func (b apiExecutionGrantStorage) checkGuestExecutionSource(record *persistence.CommandRecord, read func(string) (*controlplane.GuestExecution, error)) error {
	if b.lifecycle == nil || record == nil || record.Command.Type != "guest_execution" || b.sourceErr != nil && *b.sourceErr != nil {
		return nil
	}
	cmd := record.Command
	var err error
	if cmd.Execution == nil || validatePathSegment(cmd.Execution.ID, "execution") != nil {
		err = persistence.ErrConflict
	} else {
		var x *controlplane.GuestExecution
		x, err = read(cmd.Execution.ID)
		if errors.Is(err, persistence.ErrNotFound) {
			err = persistence.ErrConflict
		}
		if err == nil && (x == nil || x.ID != cmd.Execution.ID || x.AgentID != cmd.AgentID || x.CommandID != cmd.ID || x.Topology != cmd.Execution.Topology || x.Node != cmd.Execution.Node || !x.CreatedAt.Equal(cmd.Execution.CreatedAt) || x.CancelRequested || x.Status != controlplane.GuestExecutionQueued && x.Status != controlplane.GuestExecutionRunning) {
			err = persistence.ErrConflict
		}
	}
	return b.rejectGrantSource(err)
}

func (b apiExecutionGrantStorage) rejectGrantSource(err error) error {
	if err != nil && b.sourceErr != nil && (errors.Is(err, persistence.ErrConflict) || errors.Is(err, persistence.ErrCorruptRecord) || errors.Is(err, persistence.ErrUnsupportedVersion) || errors.Is(err, persistence.ErrInvalidRecord)) {
		*b.sourceErr = err
		*b.commitCommand = false
		return nil
	}
	return err
}
