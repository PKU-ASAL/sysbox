package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestPostgresRetryAndGuestDispatchAcceptance(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedPostgresTestDSN(t)
	s := &controlstore.Postgres{DSN: dsn}
	pool, err := s.PoolFor(ctx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	retry := controlstore.DispatchRetryRecord{CommandID: "retry", Failures: 2, Next: time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute), LastError: "offline"}
	require.NoError(t, s.SaveDispatchRetry(ctx, retry))
	reopened := &controlstore.Postgres{DSN: dsn}
	other, err := reopened.PoolFor(ctx)
	require.NoError(t, err)
	t.Cleanup(other.Close)
	got, err := reopened.LoadDispatchRetry(ctx, retry.CommandID)
	require.NoError(t, err)
	require.Equal(t, retry.Failures, got.Failures)
	require.True(t, retry.Next.Equal(got.Next))
	require.NoError(t, reopened.ClearDispatchRetry(ctx, retry.CommandID))
	_, err = s.LoadDispatchRetry(ctx, retry.CommandID)
	require.ErrorIs(t, err, persistence.ErrNotFound)
	x := controlplane.GuestFileOperation{ID: "file", CommandID: "file-command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued"}
	c := controlplane.AgentCommand{TopologyUID: "owner", ID: x.CommandID, AgentID: "agent", Type: "guest_file_put", Status: "queued", Protocol: controlplane.AgentProtocolVersion, FilePut: &controlplane.GuestFilePut{ID: x.ID, Topology: x.Topology, Node: x.Node}}
	require.NoError(t, s.CreateGuestFileDispatch(ctx, x, c))
	lease, ok, err := reopened.AcquireAgentCommandLease(ctx, "agent", c.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c.FilePut, lease.FilePut)
	conflict := x
	conflict.ID = "rolled-back"
	c.FilePut.ID = conflict.ID
	require.Error(t, s.CreateGuestFileDispatch(ctx, conflict, c))
	_, err = s.GetGuestFileOperation(ctx, conflict.ID)
	require.ErrorIs(t, err, persistence.ErrNotFound)
}
