package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

func TestWorkspaceDeletePreservesUnresolvedLifecycleAction(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	w := s.workspaceService()
	require.NoError(t, w.UpsertProject(ctx, "lab", map[string][]byte{"field.sysbox.hcl": []byte("test")}))
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	a := runtime.ActionRecord{ID: "pending", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "web-uid", Generation: 1}
	require.NoError(t, repo.CommitActionIntent(ctx, a))
	for _, force := range []bool{false, true} {
		require.ErrorIs(t, w.Delete(ctx, "lab", force), persistence.ErrConflict)
		_, err := w.HCL("lab")
		require.NoError(t, err)
		journal, err := repo.LoadActions(ctx, "lab")
		require.NoError(t, err)
		require.Len(t, journal, 1)
	}
}

func TestWorkspaceDeleteRetainsRepositoryLockAndInvalidatesOwner(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	w := s.workspaceService()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	lockPath := filepath.Join(w.StateFile("lab")+".lifecycle", "lab.lock")
	before, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.NoError(t, w.Delete(ctx, "lab", false))
	after, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "unlinking a held flock would allow independent writers")
	next, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NotEqual(t, st.TopologyUID, next.TopologyUID)
}
