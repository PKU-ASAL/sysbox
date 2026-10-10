package persistence

import (
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHCompletionCommitsExactResultAfterCommandRetirement(t *testing.T) {
	req := substrate.SSHAccessMutation{ID: "delete", ExpectedRevision: 2, Delete: true, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	op := controlplane.NodeOperation{ID: req.ID, ActionID: req.ID, CommandID: "command", AgentID: "agent", Topology: "lab", Node: "web", ResourceUID: "node", Generation: 1, Operation: "guest_ssh_access_delete", Status: controlplane.NodeOperationStatusRunning, SSHMutation: &req}
	cmd := &CommandRecord{Command: controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ActionID: req.ID, ResourceUID: "node", Generation: 1, Operation: op, Attempt: 1}}
	result := substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{ID: req.ID, Digest: digest, NodeID: "web", User: "ubuntu", AccessID: "admin", Revision: 3, BootID: "guest", TopologyUID: "owner", ResourceUID: "node", Generation: 1, Epoch: 7}}
	receipt := controlplane.SignedExecutionStopReceipt{Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: 7, Operation: op.Operation, Digest: digest, TargetBootID: "guest"}, HostBootID: "host", GuestBootID: "guest", ReceiptID: "receipt", StoppedAt: time.Now().UTC(), Quiescence: "confirmed", SSHResult: &result}
	for _, retired := range []bool{false, true} {
		copyCommand := *cmd
		copyCommand.Retired = retired
		if retired {
			copyCommand.Command.Operation = controlplane.NodeOperation{}
		}
		done, err := CompleteSSHAccessFromHost(op, &copyCommand, "agent", op.ID, receipt)
		require.NoError(t, err)
		require.Equal(t, controlplane.NodeOperationStatusDone, done.Status)
		require.Equal(t, &result, done.SSHResult)
		again, err := CompleteSSHAccessFromHost(done, &copyCommand, "agent", op.ID, receipt)
		require.NoError(t, err)
		require.Equal(t, done, again)
	}
	for _, field := range []string{"user", "revision", "node", "scope", "attempt"} {
		bad := receipt
		badResult := result
		bad.SSHResult = &badResult
		switch field {
		case "user":
			badResult.Handle.User = "root"
		case "revision":
			badResult.Handle.Revision++
		case "node":
			badResult.Handle.NodeID = "other"
		case "scope":
			bad.Grant.Scope.TopologyUID = "other"
		case "attempt":
			bad.Grant.Attempt++
		}
		_, err := CompleteSSHAccessFromHost(op, cmd, "agent", op.ID, bad)
		require.Error(t, err, field)
	}
	report := op
	report.Status = controlplane.NodeOperationStatusDone
	require.ErrorIs(t, ValidateNodeOperationTransition(op, report), ErrConflict, "ordinary CAS cannot finish an SSH effect without host evidence")
	report.SSHResult = &result
	report.Status = controlplane.NodeOperationStatusDone
	require.ErrorIs(t, ValidateNodeOperationTransition(op, report), ErrConflict)
	changed := op
	changedRequest := req
	changedRequest.ExpectedRevision++
	changed.SSHMutation = &changedRequest
	require.ErrorIs(t, ValidateNodeOperationTransition(op, changed), ErrConflict)
}
