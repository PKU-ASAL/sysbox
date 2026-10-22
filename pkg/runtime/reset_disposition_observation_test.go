package runtime

import (
	"context"
	"encoding/json"
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
