package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestWorkerCannotCompleteSSHAccessDespiteLiveCommandLease(t *testing.T) {
	for _, operation := range []string{"ssh_access_apply", "ssh_access_delete"} {
		for _, status := range []string{controlplane.NodeOperationStatusDone, controlplane.NodeOperationStatusFailed} {
			t.Run(operation+"/"+status, func(t *testing.T) {
				now := time.Now().UTC()
				current := controlplane.NodeOperation{ID: "op", AgentID: "agent", CommandID: "command", Topology: "lab", Node: "web", Operation: operation, Status: controlplane.NodeOperationStatusQueued, ResourceUID: "resource", Generation: 1, ActionID: "action"}
				command := &CommandRecord{Command: controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", Operation: current, Status: controlplane.AgentCommandStatusLeased, LeaseOwner: "worker", LeaseUntil: now.Add(time.Minute), Attempt: 1, ActionID: "action", ResourceUID: "resource", Generation: 1}}
				report := current
				report.Status = status
				report.EndedAt = now
				report.LeaseOwner = "worker"
				report.Attempt = 1
				_, err := CompleteNodeOperation(current, command, "agent", "op", report, now)
				require.ErrorIs(t, err, ErrConflict)
			})
		}
	}
}
