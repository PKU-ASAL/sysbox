package hostsupervisor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestReturnedDestroyFailureRequiresAllDeletionEffectsReturned(t *testing.T) {
	now := time.Now().UTC()
	run := controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy", Status: controlplane.RunFailed, QueuedAt: now, EndedAt: now, Revision: "revision"}
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: "owner", ResourceUID: "node", Generation: 1, ExternalID: "immutable-id"}
	action := runtime.ActionRecord{ID: "delete", Topology: "lab", RunID: run.ID, RunQueuedAt: now, RunRevision: run.Revision, Resource: resource.Address, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation, Candidate: &resource, Action: model.PlanActionDelete, Status: runtime.ActionUncertain, EffectComplete: true}
	for _, scenario := range []string{"returned", "committed", "unknown", "foreign-run", "foreign-incarnation", "create", "missing-candidate", "changed-identity", "other-pending", "no-actions", "unknown-child"} {
		t.Run(scenario, func(t *testing.T) {
			a := action
			children := &RunChildren{}
			actions := []runtime.ActionRecord{a}
			switch scenario {
			case "committed":
				a.Status = runtime.ActionSucceeded
			case "unknown":
				a.EffectComplete = false
			case "foreign-run":
				a.RunID = "other"
			case "foreign-incarnation":
				a.RunQueuedAt = now.Add(time.Second)
			case "create":
				a.Action = model.PlanActionCreate
			case "missing-candidate":
				a.Candidate = nil
			case "changed-identity":
				a.Generation++
			case "other-pending":
				actions = append(actions, runtime.ActionRecord{ID: "pending", RunID: "other", Status: runtime.ActionUncertain})
			case "unknown-child":
				children.unknownAdmission = true
			}
			actions[0] = a
			if scenario == "no-actions" {
				actions = nil
			}
			err := FinishReturnedDestroyFailure(context.Background(), children, &run, actions)
			var stopped *quiescentRunFailure
			if scenario == "returned" || scenario == "committed" {
				require.ErrorAs(t, err, &stopped)
				var evidence controlplane.Run
				require.NoError(t, json.Unmarshal(stopped.evidence, &evidence))
				require.Equal(t, controlplane.RunFailed, evidence.Status)
				require.True(t, evidence.Recoverable)
				require.Nil(t, evidence.Cleanup)
			} else {
				require.NotErrorAs(t, err, &stopped)
			}
		})
	}
}
