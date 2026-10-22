package persistence

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRecoveryObligationCanFollowLaterVerifiedReplacement(t *testing.T) {
	resource := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
	require.NoError(t, resource.SetProviderState(json.RawMessage(`{"identity":"source"}`)))
	choice := controlplane.IntentDispositionAction{ActionID: "create", Decision: "retain-incomplete", ResourceUID: "resource", Generation: 1, ExternalID: "source"}
	old := IntentDispositionArchive{Decision: controlplane.IntentDispositionDecision{ID: "first", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{choice}}, Observation: controlplane.RunRecoveryReport{TopologyUID: "owner"}, Actions: map[string]runtime.ActionRecord{"create": {ID: "create", Resource: resource.Address, Candidate: &resource}}}
	replacement := resource
	replacement.Generation = 2
	replacement.ExternalID = "replacement"
	targetChoice := controlplane.IntentDispositionAction{ActionID: "reset", Decision: "retain-replacement-incomplete", ResourceUID: "resource", Generation: 2, ExternalID: "replacement"}
	target := IntentDispositionArchive{Decision: controlplane.IntentDispositionDecision{ID: "second", ExpectedSerial: 2, Actions: []controlplane.IntentDispositionAction{targetChoice}}, Observation: controlplane.RunRecoveryReport{TopologyUID: "owner"}, Actions: map[string]runtime.ActionRecord{"reset": {ID: "reset", Resource: resource.Address, Candidate: &resource, Replacement: &replacement}}}
	a := newAggregate()
	a.IntentDispositions = map[string]IntentDispositionArchive{"first": old, "second": target}
	migrateIntentRecoveryRequirements(a, "second")
	require.Equal(t, IntentRecoveryTransfer{DispositionID: "second", ActionID: "reset"}, a.IntentDispositions["first"].Transfers["create"])
	require.True(t, validIntentRecoveryTransfer(old, choice, target, targetChoice))
	target.Decision.ExpectedSerial = 1
	require.False(t, validIntentRecoveryTransfer(old, choice, target, targetChoice), "no backward or same-serial transfer")
	target.Decision.ExpectedSerial = 2
	changed := *target.Actions["reset"].Candidate
	changed.Driver = "libvirt"
	entry := target.Actions["reset"]
	entry.Candidate = &changed
	target.Actions["reset"] = entry
	require.False(t, validIntentRecoveryTransfer(old, choice, target, targetChoice))
}
