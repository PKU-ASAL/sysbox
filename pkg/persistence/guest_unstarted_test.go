package persistence

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestUnstartedCancellationRequiresOriginalAdmissionIdentity(t *testing.T) {
	for _, kind := range []string{"missing", "node", "scope", "authority"} {
		t.Run(kind, func(t *testing.T) {
			x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", CreatedAt: time.Now().UTC()}
			cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
			raw, err := EncodeGuestExecutionDispatchRecord(x, cmd)
			require.NoError(t, err)
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &object))
			object["cancel_requested"] = json.RawMessage("true")
			original := cmd
			switch kind {
			case "missing":
				delete(object, "dispatch_command")
			case "node":
				other := *cmd.Execution
				other.Node = "other"
				cmd.Execution = &other
			case "scope":
				cmd.ResourceUID = "other"
			}
			if kind == "node" || kind == "scope" {
				object["dispatch_command"], err = EncodeCommandRecord(CommandRecord{Command: cmd})
				require.NoError(t, err)
			}
			raw, err = json.Marshal(object)
			require.NoError(t, err)
			var authority []byte
			if kind == "authority" {
				authority = []byte(`{"version":99}`)
			}
			plan, err := PrepareGuestExecutionCancellation(x.ID, raw, authority, func(id string) (*CommandRecord, error) {
				if id == original.ID {
					return &CommandRecord{Command: original, Retired: true}, nil
				}
				return nil, nil
			})
			if kind == "missing" {
				require.NoError(t, err)
				require.Equal(t, "queued", plan.Execution.Status)
				require.True(t, plan.Execution.EndedAt.IsZero())
			} else {
				require.Error(t, err)
				require.Nil(t, plan)
			}
		})
	}
}
