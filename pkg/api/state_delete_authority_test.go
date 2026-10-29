package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func stateDeleteTestServer(t *testing.T, store controlstore.Store) *Server {
	t.Helper()
	s := NewServer(t.TempDir(), t.TempDir())
	// Each fixture and cold reconstruction owns its cached lifecycle pools.
	// Closing only the API store leaves these connections alive across tests.
	t.Cleanup(func() {
		s.RepositoryMu.Lock()
		defer s.RepositoryMu.Unlock()
		for _, repo := range s.Repositories {
			require.NoError(t, repo.Close())
		}
		s.Repositories = nil
	})
	s.apiStore, s.workspaces = store, nil
	switch typed := store.(type) {
	case *controlstore.Local:
		s.runsDir = typed.RunsDir
	case *controlstore.SQLite:
		s.stateBackend = "sqlite://" + typed.DbPath
	case *controlstore.Postgres:
		s.stateBackend = typed.DSN
	}
	return s
}

func TestMetadataDeleteRequiresAllExecutionStopsAndNoMaintenance(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "incarnation", "")
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: before.TopologyUID}, AgentID: "agent", SessionID: session.ID, ActionID: "failed-run", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "boot"}, time.Minute)
		require.NoError(t, err)
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "failed-run", Topology: "lab", Status: controlplane.RunFailed, QueuedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}))
		for _, force := range []bool{false, true} {
			err = s.workspaceService().Delete(ctx, "lab", force)
			require.ErrorIs(t, err, persistence.ErrConflict, "terminal Run status does not prove its effect stopped")
		}
		require.NoError(t, authority.Revoke(ctx, *grant))
		inspection := httptest.NewRecorder()
		s.ServeHTTP(inspection, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
		require.Equal(t, 200, inspection.Code, inspection.Body.String())
		var evidence struct {
			Active []struct{ ID, Kind, Status string } `json:"active_operations"`
		}
		require.NoError(t, json.Unmarshal(inspection.Body.Bytes(), &evidence))
		require.Len(t, evidence.Active, 1, "inspection must explain why terminal operation metadata cannot be deleted")
		require.Equal(t, "execution_grant", evidence.Active[0].Kind)
		require.Equal(t, grant.ActionID, evidence.Active[0].ID)
		require.Equal(t, string(controlplane.GrantRevoked), evidence.Active[0].Status)
		require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", true), persistence.ErrConflict, "revocation is not a stop receipt")
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, before.TopologyUID, after.TopologyUID)
		require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
		_, err = authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "other-topology"}, AgentID: "agent", SessionID: session.ID, ActionID: "other", CommandID: "other-command", Attempt: 1, Operation: "run_apply", Digest: "other-digest", TargetBootID: "boot"}, time.Minute)
		require.NoError(t, err)
		barrier, err := authority.BeginMaintenance(ctx, "restore", before.TopologyUID, "plan")
		require.NoError(t, err)
		require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", true), persistence.ErrConflict, "maintenance must retain the original lifecycle identity")
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, s.workspaceService().Delete(ctx, "lab", true))
	})
}

func TestMetadataDeleteLocksInitiallyAbsentAuthorityThroughCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		if _, ok := store.(*controlstore.SQLite); ok {
			t.Skip("SQLite uses one BEGIN IMMEDIATE lock for both records")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		var release func()
		var awaitBlocked func()
		switch typed := store.(type) {
		case *controlstore.Local:
			blocker := flock.New(filepath.Join(typed.RunsDir, "lab", "state.json.lifecycle", "lab.lock"))
			require.NoError(t, blocker.Lock())
			defer blocker.Close()
			release = func() { require.NoError(t, blocker.Unlock()) }
			awaitBlocked = func() {
				probe := flock.New(filepath.Join(typed.RunsDir, "_execution-authority", "authority.lock"))
				defer probe.Close()
				require.Eventually(t, func() bool {
					held, err := probe.TryLock()
					if err != nil {
						return false
					}
					if held {
						_ = probe.Unlock()
					}
					return !held
				}, time.Second, time.Millisecond)
			}
		case *controlstore.Postgres:
			pool, err := typed.PoolFor(ctx)
			require.NoError(t, err)
			blocker, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer blocker.Rollback(context.Background())
			_, err = blocker.Exec(ctx, "SELECT topology FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", "lab")
			require.NoError(t, err)
			release = func() { require.NoError(t, blocker.Rollback(ctx)) }
			awaitBlocked = func() {
				require.Eventually(t, func() bool {
					var count int
					err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND query=$1 AND wait_event_type='Lock'", "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE").Scan(&count)
					return err == nil && count > 0
				}, time.Second, time.Millisecond)
			}
		}
		done := make(chan error, 1)
		go func() { done <- s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID) }()
		awaitBlocked()
		limited, cancelAdmission := context.WithTimeout(ctx, 40*time.Millisecond)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		_, err = authority.OpenSession(limited, "agent", "first-incarnation", "")
		cancelAdmission()
		require.Error(t, err, "first authority creation cannot pass a deletion waiting for lifecycle commit")
		release()
		require.NoError(t, <-done)
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.NotEqual(t, before.TopologyUID, after.TopologyUID)
		_, err = authority.OpenSession(ctx, "agent", "first-incarnation", "")
		require.NoError(t, err)
	})
}
