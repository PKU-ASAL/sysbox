package api

import (
	"errors"
	"reflect"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// The caller reads the current operation inside the authority/command write
// boundary. A retained queued dispatch is not proof its operation is live.
func (b apiExecutionGrantStorage) checkGuestSSHSource(record *persistence.CommandRecord, read func(string) (*controlplane.NodeOperation, error)) error {
	if record == nil || record.Command.Type != "node_operation" || !controlplane.IsGuestSSHAccessOperation(record.Command.Operation.Operation) || b.sourceErr != nil && *b.sourceErr != nil {
		return nil
	}
	cmd := record.Command
	current, err := read(cmd.Operation.ID)
	if errors.Is(err, persistence.ErrNotFound) {
		err = persistence.ErrConflict
	}
	if err == nil && (current == nil || current.ID != cmd.ActionID || current.ActionID != cmd.ActionID || current.CommandID != cmd.ID ||
		current.AgentID != cmd.AgentID || current.Topology != cmd.Operation.Topology || current.Node != cmd.Operation.Node ||
		current.ResourceUID != cmd.ResourceUID || current.Generation != cmd.Generation || current.Operation != cmd.Operation.Operation ||
		!current.CreatedAt.Equal(cmd.Operation.CreatedAt) || current.SSHResult != nil || current.SSHAccess != nil || current.Adoption != nil ||
		(current.Status != controlplane.NodeOperationStatusQueued && current.Status != controlplane.NodeOperationStatusRunning) ||
		!reflect.DeepEqual(current.SSHMutation, cmd.Operation.SSHMutation)) {
		err = persistence.ErrConflict
	}
	return b.rejectGrantSource(err)
}
