package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCanonicalUnlockRevokesAndWaitsForHostStopsAcrossRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID}, AgentID: "agent", SessionID: session.ID, ActionID: "old", CommandID: "old", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "boot"}, time.Minute)
		require.NoError(t, err)
		result, err := s.forceUnlockAuthority(ctx, "lab", "unlock-one")
		require.NoError(t, err)
		require.Equal(t, "waiting_for_stop", result.Status)
		require.Error(t, authority.Authorize(ctx, *grant))
		info, err := s.inspectTopologyAuthority(ctx, "lab")
		require.NoError(t, err)
		require.True(t, info.Locked)
		require.Len(t, info.Grants, 1)
		require.Equal(t, controlplane.GrantRevoked, info.Grants[0].State)
		require.NoError(t, authority.AcceptHostStop(ctx, *grant, "original-host-stop"))
		restarted := stateDeleteTestServer(t, store)
		done, err := restarted.forceUnlockAuthority(ctx, "lab", "unlock-one")
		require.NoError(t, err)
		require.Equal(t, "unlocked", done.Status)
		require.Equal(t, result.Barrier.Epoch, done.Barrier.Epoch)
		require.True(t, done.Barrier.Released)
		replay, err := restarted.forceUnlockAuthority(ctx, "lab", "unlock-one")
		require.NoError(t, err)
		require.Equal(t, done, replay)
		require.Error(t, authority.Authorize(ctx, *grant))
	})
}

func TestCanonicalLockHTTPReportsExecutionAuthority(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/topologies/lab/state/lock", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "execution-authority")
}
