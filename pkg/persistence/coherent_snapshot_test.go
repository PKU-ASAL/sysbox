package persistence

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type stateActionsReader interface {
	LoadStateAndActions(context.Context, string) (*state.State, []runtime.ActionRecord, error)
}

// Models a create committing immediately after a repository transaction ends.
type completingSnapshotBackend struct {
	backend
	before, after *aggregate
	reads         int
}

func (b *completingSnapshotBackend) name() string { return "snapshot-test" }
func (b *completingSnapshotBackend) update(_ context.Context, _ string, fn func(*aggregate) error) error {
	b.reads++
	if b.reads == 1 {
		return fn(b.before)
	}
	return fn(b.after)
}

func TestLoadStateAndActionsUsesOneCoherentView(t *testing.T) {
	before := newAggregate()
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: before.State.TopologyUID, ResourceUID: "web-uid", Generation: 1, ExternalID: "node-1"}
	record := runtime.ActionRecord{ID: "create", Revision: 1, Topology: "lab", RunID: "run", OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Resource: resource.Address, Generation: 1, Action: controlplane.PlanActionCreate, Status: runtime.ActionUncertain, Candidate: &resource}
	before.Actions[record.ID] = record
	after := newAggregate()
	after.State.TopologyUID = before.State.TopologyUID
	after.State.Resources = []state.Resource{resource}
	after.Serial = 1
	record.Status, record.EffectComplete, record.Revision = runtime.ActionSucceeded, true, 2
	after.Actions[record.ID] = record
	b := &completingSnapshotBackend{before: before, after: after}
	repo := &Repository{backend: b}
	reader, ok := any(repo).(stateActionsReader)
	require.True(t, ok, "repository must expose an atomic state/action snapshot")
	st, actions, err := reader.LoadStateAndActions(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, 1, b.reads)
	require.Empty(t, st.Resources)
	require.Equal(t, runtime.ActionUncertain, actions[0].Status)
	require.Zero(t, st.Meta.Serial)
	require.Zero(t, before.Serial, "reading must not advance the durable serial")
	st, actions, err = reader.LoadStateAndActions(context.Background(), "lab")
	require.NoError(t, err)
	require.Len(t, st.Resources, 1)
	require.Equal(t, runtime.ActionSucceeded, actions[0].Status)
	require.EqualValues(t, 1, st.Meta.Serial)
	require.EqualValues(t, 1, after.Serial)
	st.Resources[0].ExternalID = "mutated"
	actions[0].Candidate.ExternalID = "mutated"
	require.Equal(t, "node-1", after.State.Resources[0].ExternalID)
	require.Equal(t, "node-1", after.Actions[record.ID].Candidate.ExternalID)
}
