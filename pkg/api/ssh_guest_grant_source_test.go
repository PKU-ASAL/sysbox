package api

import (
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHSourceRejectsCurrentStateIndependentOfCommandSnapshot(t *testing.T) {
	req := substrate.SSHAccessMutation{ID: "op", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	op := controlplane.NodeOperation{ID: "op", ActionID: "op", AgentID: "agent", CommandID: "cmd", Topology: "lab", Node: "web", Operation: "guest_ssh_access_delete", ResourceUID: "resource", Generation: 1, CreatedAt: time.Now(), Status: controlplane.NodeOperationStatusQueued, SSHMutation: &req}
	record := &persistence.CommandRecord{Command: controlplane.AgentCommand{ID: "cmd", Type: "node_operation", ActionID: "op", AgentID: "agent", ResourceUID: "resource", Generation: 1, Operation: op}}
	for _, scenario := range []string{"queued", "running", "done", "failed", "missing", "mutation", "incarnation", "command", "generation", "result"} {
		t.Run(scenario, func(t *testing.T) {
			current := op
			switch scenario {
			case "running":
				current.Status = controlplane.NodeOperationStatusRunning
			case "done":
				current.Status = controlplane.NodeOperationStatusDone
			case "failed":
				current.Status = controlplane.NodeOperationStatusFailed
			case "mutation":
				changed := req
				changed.ExpectedRevision++
				current.SSHMutation = &changed
			case "incarnation":
				current.CreatedAt = current.CreatedAt.Add(time.Second)
			case "command":
				current.CommandID = "other"
			case "generation":
				current.Generation++
			case "result":
				current.SSHResult = &substrate.SSHAccessResult{}
			}
			var sourceErr error
			commit := true
			storage := controlstore.AuthorityStorage{SourceErr: &sourceErr, CommitCommand: &commit}
			err := storage.CheckGuestSSHSource(record, func(id string) (*controlplane.NodeOperation, error) {
				require.Equal(t, "op", id)
				if scenario == "missing" {
					return nil, persistence.ErrNotFound
				}
				return &current, nil
			})
			if scenario == "queued" || scenario == "running" {
				require.NoError(t, err)
				require.NoError(t, sourceErr)
				require.True(t, commit)
			} else {
				require.ErrorIs(t, err, persistence.ErrConflict)
				require.True(t, commit, "transaction abort means no lease write occurs")
			}
		})
	}
}
