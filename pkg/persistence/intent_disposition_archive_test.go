package persistence

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIntentDispositionPostimagePreservesUnknownAndExactResource(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	action := runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", RunQueuedAt: time.Now().UTC(), RunRevision: "revision", Resource: address.Resource("sysbox_node", "node"), Action: controlplane.PlanActionCreate, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}}
	a.Actions[action.ID] = action
	d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: action.ID, Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	archive, err := intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, action, archive[action.ID])
	require.Equal(t, runtime.ActionUncertain, a.Actions[action.ID].Status)
	require.Equal(t, "intent:decision", a.Actions[action.ID].DispositionID)
	require.Len(t, a.State.Resources, 1)
	require.Equal(t, "external", a.State.Resources[0].ExternalID)
	require.Equal(t, state.ResourceUnknown, a.State.Resources[0].Status)
	require.Equal(t, int64(2), a.Serial)
	require.Equal(t, int64(1), a.Actions[action.ID].ExpectedSerial)
}
func TestIntentDispositionPostimageRejectsConflictsWithoutPartialMutation(t *testing.T) {
	for _, kind := range []string{"new-generation", "different-driver", "missing-private", "serial-overflow"} {
		t.Run(kind, func(t *testing.T) {
			a := newAggregate()
			a.State.TopologyUID = "owner"
			a.Serial = 1
			resource := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
			action := runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", Resource: resource.Address, Action: controlplane.PlanActionCreate, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &resource}
			a.Actions[action.ID] = action
			existing := resource
			switch kind {
			case "new-generation":
				existing.Generation = 2
				a.State.Resources = []state.Resource{existing}
			case "different-driver":
				existing.Driver = "libvirt"
				a.State.Resources = []state.Resource{existing}
			case "missing-private":
				resource.Private = nil
			case "serial-overflow":
				a.Serial = 9223372036854775807
			}
			before, err := json.Marshal(a)
			require.NoError(t, err)
			d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: a.Serial, Actions: []controlplane.IntentDispositionAction{{ActionID: "action", Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
			_, err = intentDispositionPostimage(a, d, time.Now().UTC())
			require.Error(t, err)
			after, err := json.Marshal(a)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}

func TestIntentDispositionPostimageRetainsOneExistingResourceDeterministically(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	resource := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, CreatedAt: at.Add(-time.Hour)}
	a.State.Resources = []state.Resource{resource}
	a.Actions["action"] = runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", Resource: resource.Address, Action: controlplane.PlanActionDelete, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &resource}
	d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: "action", Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	var cold aggregate
	require.NoError(t, json.Unmarshal(raw, &cold))
	_, err = intentDispositionPostimage(a, d, at)
	require.NoError(t, err)
	_, err = intentDispositionPostimage(&cold, d, at)
	require.NoError(t, err)
	first, err := json.Marshal(a)
	require.NoError(t, err)
	second, err := json.Marshal(&cold)
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(second))
	require.Len(t, a.State.Resources, 1)
	require.Equal(t, resource.CreatedAt, a.State.Resources[0].CreatedAt)
	require.Equal(t, at, a.State.Resources[0].UpdatedAt)
}
