package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/substrate"
)

type checkpointGuestConn struct {
	recordingConn
	onStart func()
	starts  int
}

func (c *checkpointGuestConn) Start(context.Context, substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	c.starts++
	c.onStart()
	return substrate.ExecutionHandle{ID: "execution-1", NodeID: "node", Generation: 1, PID: 42}, nil
}

func TestCreatePersistsIncompleteCandidateBeforeGuestStart(t *testing.T) {
	e, d, cfg, addr := nodeGuardExecutor(t, nil)
	d.privateState = json.RawMessage(`{"connection":{"endpoint":"guest-vsock","cid":42}}`)
	cfg.Provisioners = []config.ProvisionerConfig{{Type: "exec", Program: "worker", Shell: "none", Background: true}}
	conn := &checkpointGuestConn{onStart: func() {
		records, err := e.actions.LoadActions(context.Background(), e.topology)
		require.NoError(t, err)
		require.Len(t, records, 1)
		r := records[0]
		require.NotNil(t, r.Candidate, "guest supervisor needs the durable candidate to resolve node identity")
		require.Equal(t, ActionUncertain, r.Status)
		require.False(t, r.EffectComplete)
		require.Equal(t, r.OwnerUID, r.Candidate.OwnerUID)
		require.Equal(t, r.ResourceUID, r.Candidate.ResourceUID)
		require.Equal(t, r.Generation, r.Candidate.Generation)
		require.NotEmpty(t, r.Candidate.ExternalID)
		require.Equal(t, "sysbox_node", r.Candidate.ResourceType)
		require.NotZero(t, r.Candidate.SchemaVersion)
		private, err := r.Candidate.ProviderState()
		require.NoError(t, err)
		require.JSONEq(t, string(d.privateState), string(private))
		persisted, err := e.actions.LoadState(context.Background(), e.topology)
		require.NoError(t, err)
		require.Nil(t, persisted.FindResource(addr), "incomplete creates must not publish resource state")
	}}
	d.guestConn = conn
	require.NoError(t, e.Apply(context.Background(), singleConvergencePlan(addr, model.PlanActionCreate)))
	require.Equal(t, 1, conn.starts)
}

func TestCreateCheckpointFailurePreventsGuestEffects(t *testing.T) {
	e, d, cfg, addr := nodeGuardExecutor(t, nil)
	cfg.Provisioners = []config.ProvisionerConfig{{Type: "exec", Program: "worker", Shell: "none", Background: true}}
	conn := &checkpointGuestConn{onStart: func() {}}
	d.guestConn = conn
	want := errors.New("candidate checkpoint unavailable")
	e.actions.(*memoryActions).outcomeErr = want
	require.ErrorIs(t, e.Apply(context.Background(), singleConvergencePlan(addr, model.PlanActionCreate)), want)
	require.Zero(t, conn.starts)
	require.Zero(t, d.entries)
	require.Nil(t, e.state.FindResource(addr))
}
