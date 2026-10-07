package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGrantRecoveryInspectSurvivesRetiredSessionAndStopReplayAfterSuccessor(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := repo.OpenSession(ctx, "agent", "one", "")
		require.NoError(t, err)
		request := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "guest_task", Digest: "digest", TargetBootID: "guest-boot"}
		g, err := repo.Issue(ctx, request, time.Minute)
		require.NoError(t, err)
		nextSession, err := repo.OpenSession(ctx, "agent", "two", session.ID)
		require.NoError(t, err)
		got, err := repo.InspectGrant(ctx, "agent", g.Scope)
		require.NoError(t, err)
		require.Equal(t, g.Epoch, got.Epoch)
		_, err = repo.InspectGrant(ctx, "other-agent", g.Scope)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		_, err = repo.Renew(ctx, *g, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		request.SessionID = nextSession.ID
		request.ActionID = "next"
		_, err = repo.Issue(ctx, request, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.NoError(t, repo.Revoke(ctx, *g))
		require.NoError(t, repo.AcknowledgeStop(ctx, *g, "signed-receipt"))
		next, err := repo.Issue(ctx, request, time.Minute)
		require.NoError(t, err)
		require.Greater(t, next.Epoch, g.Epoch)
		require.NoError(t, repo.AcknowledgeStop(ctx, *g, "signed-receipt"))
		require.ErrorIs(t, repo.AcknowledgeStop(ctx, *g, "different-receipt"), persistence.ErrConflict)
		require.NoError(t, repo.Authorize(ctx, *next))
	})
}
