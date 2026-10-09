package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMaintenanceQuiescenceBlocksActiveConsoleSession(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	sess := controlplane.ConsoleSession{ID: "console", Topology: "lab", Workspace: "other-workspace", Status: controlplane.ConsoleSessionStatusRunning, CreatedAt: time.Now().UTC(), StartedAt: time.Now().UTC()}
	require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), sess))
	require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
}

func TestConsoleLostOnRestartStillBlocksMaintenanceAndDeletion(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		sess := controlplane.ConsoleSession{ID: "console", AgentID: "agent", Topology: "lab", Workspace: "project", Status: controlplane.ConsoleSessionStatusRunning, CreatedAt: time.Now().UTC(), StartedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(ctx, sess))
		_ = newConsoleSessionHub(store)
		lost, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.ConsoleSessionStatusLost, lost.Status)
		require.False(t, lost.EndedAt.IsZero(), "restart marks transport loss time, not a verified remote stop")
		require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(ctx, "lab"), persistence.ErrConflict)
		require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", true), persistence.ErrConflict)
	})
}

func TestConsoleEvidenceRequiresKnownTerminalStatusAndTimes(t *testing.T) {
	for _, status := range []string{"queued", "running", "lost", "unknown", "closed", "failed", "cancelled", "denied"} {
		t.Run(status, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			sess := controlplane.ConsoleSession{ID: "session", Topology: "lab", Status: status, CreatedAt: time.Now().UTC()}
			require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), sess))
			require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
			sess.EndedAt = time.Now().UTC()
			require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), sess))
			err := s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab")
			if status == "lost" || status == "unknown" || status == "queued" || status == "running" {
				require.ErrorIs(t, err, persistence.ErrConflict)
			} else {
				require.NoError(t, err)
			}
			sess.EndedAt = sess.CreatedAt.Add(-time.Second)
			require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), sess))
			require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
			sess.CreatedAt = time.Time{}
			sess.EndedAt = time.Now().UTC()
			require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), sess))
			require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
		})
	}
}

func TestConsoleActivityDoesNotBlockUnrelatedTopology(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), controlplane.ConsoleSession{ID: "lost", Topology: "other", Workspace: "lab", Status: controlplane.ConsoleSessionStatusLost}))
	require.NoError(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"))
}
