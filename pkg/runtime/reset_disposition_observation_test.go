package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

type resetDispositionObserver struct {
	*portTestSubstrate
	checked []string
}

func (s *resetDispositionObserver) HostFenceScope(context.Context) (string, error) {
	return "local-only", nil
}
func (s *resetDispositionObserver) HostFenceDeletionAbsent(_ context.Context, h substrate.NodeHandle) error {
	s.checked = append(s.checked, h.ID)
	return nil
}
func TestResetSourceAbsenceUsesProviderResidualProof(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	sub := &resetDispositionObserver{portTestSubstrate: &portTestSubstrate{name: "reset-absence", nodeObservation: substrate.NodeObservation{Status: substrate.NodeStatusMissing}}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: sub.name, Version: "test", Node: sub, NodeState: sub}))
	addr := address.Resource("sysbox_node", "node")
	source := state.Resource{Address: addr, Driver: sub.name, ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
	require.NoError(t, source.SetProviderState(json.RawMessage(`{}`)))
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner", Resources: []state.Resource{source}}
	action := ActionRecord{ID: "reset", RunID: "run", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 2, Candidate: &source, Phase: "destroying", ReplayForbidden: true}
	repo := &memoryActions{state: st, actions: []ActionRecord{action}}
	_, items, err := ObserveRunActions(context.Background(), repo, "lab", "run")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].ResetSource)
	require.Equal(t, "absent", items[0].ResetSource.Status)
	require.True(t, items[0].ResetSource.DeleteResidueAbsent)
	require.Equal(t, []string{"source"}, sub.checked)
	require.Equal(t, "confirm-absence", PlanIntentDisposition(action, items).ResourceDecision)
}

func TestIntentObservationDistinguishesStoppedPresenceFromHealth(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	sub := &resetDispositionObserver{portTestSubstrate: &portTestSubstrate{name: "stopped-intent", nodeObservation: substrate.NodeObservation{Exists: true, Status: substrate.NodeStatusExited, ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: sub.name, Version: "test", Node: sub, NodeState: sub}))
	addr := address.Resource("sysbox_node", "node")
	source := state.Resource{Address: addr, Driver: sub.name, ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
	require.NoError(t, source.SetProviderState(json.RawMessage(`{}`)))
	action := ActionRecord{ID: "a", RunID: "run", Resource: addr, Action: controlplane.PlanActionCreate, Status: ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Candidate: &source, ReplayForbidden: true}
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	observed := observeIntentResource(context.Background(), action, &source, st)
	require.Equal(t, "present", observed.Status)
	require.Equal(t, ActionUncertain, action.Status)
	sub.nodeObservation.OwnerUID = "foreign"
	require.Equal(t, "unknown", observeIntentResource(context.Background(), action, &source, st).Status)
}

func TestAttachmentAbsenceRequiresExactProviderResidueProof(t *testing.T) {
	oldRegistry := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = oldRegistry })
	sub := &attachmentAbsentObserver{resetDispositionObserver: resetDispositionObserver{portTestSubstrate: &portTestSubstrate{name: "attachment-absence", nodeObservation: substrate.NodeObservation{Status: substrate.NodeStatusMissing}}}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: sub.name, Version: "test", Node: sub, NodeState: sub}))
	addr := address.Resource("sysbox_node", "node")
	r := state.Resource{Address: addr, Driver: sub.name, ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Attachments: []state.Attachment{{Name: "lan", Node: addr, Driver: sub.name, DriverState: json.RawMessage(`{"proof":"exact"}`)}}}
	require.NoError(t, r.SetProviderState(json.RawMessage(`{}`)))
	a := ActionRecord{ID: "delete", RunID: "run", Resource: addr, Action: controlplane.PlanActionDelete, Status: ActionUncertain, OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: 1, Candidate: &r}
	st := &state.State{TopologyUID: "owner", Resources: []state.Resource{r}}
	o := observeIntentResource(context.Background(), a, &r, st)
	require.Equal(t, 1, o.AttachmentCount)
	require.True(t, o.AttachmentResidueAbsent)
	require.Equal(t, []string{"lan"}, sub.attachments)
	require.Equal(t, []string{"effect_stop_proof_required"}, PlanIntentDisposition(a, []controlplane.RecoveredResource{o}).Blockers)
	second := r.Attachments[0]
	second.Name = "wan"
	second.DriverState = json.RawMessage(`{"unproven":true}`)
	r.Attachments = append(r.Attachments, second)
	o = observeIntentResource(context.Background(), a, &r, st)
	require.False(t, o.AttachmentResidueAbsent, "one proven attachment cannot hide an unproven second")
	sub.fail = true
	o = observeIntentResource(context.Background(), a, &r, st)
	require.False(t, o.AttachmentResidueAbsent)
	require.Contains(t, PlanIntentDisposition(a, []controlplane.RecoveredResource{o}).Blockers, "attachment_absence_proof_required")
}

type attachmentAbsentObserver struct {
	resetDispositionObserver
	attachments []string
	fail        bool
}

func (s *attachmentAbsentObserver) HostFenceAttachmentAbsent(_ context.Context, _ substrate.NodeHandle, name string, raw json.RawMessage) error {
	s.attachments = append(s.attachments, name)
	if s.fail || string(raw) != `{"proof":"exact"}` {
		return fmt.Errorf("attachment residue unknown")
	}
	return nil
}
