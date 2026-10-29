package api

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func leaseFileCompletion(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	op, err := s.apiStore.GetGuestFileOperation(context.Background(), id)
	require.NoError(t, err)
	lease, ok, err := s.apiStore.AcquireAgentCommandLease(context.Background(), op.AgentID, op.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	return map[string]any{"action_id": lease.ActionID, "resource_uid": lease.ResourceUID, "generation": lease.Generation, "lease_epoch": lease.LeaseEpoch, "lease_owner": lease.LeaseOwner, "attempt": lease.Attempt}
}
