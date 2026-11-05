package hostexec

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestInitialApplyAcceptsQueuedBeforeAssignment(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = ""
	env, err := Open(cfg)
	require.NoError(t, err)
	defer env.Close()
	ctx := context.Background()
	repo, err := env.Repository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	now := time.Now().UTC()
	run := controlplane.Run{ID: "first", Topology: "lab", Op: "apply", Revision: "revision", Status: controlplane.RunQueued, QueuedAt: now}
	require.NoError(t, env.Store.SaveRun(ctx, run))
	run.MarkAssigned("host", now)
	require.NoError(t, env.Store.SaveRun(ctx, run))
	claimed, ok, err := env.Store.ClaimRun(ctx, run.ID, "host", "lease", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	run = *claimed
	grant := initialApplyGrant(t, env, run, st.TopologyUID)
	require.NoError(t, env.initializeFirstApply(transport.WithParentExecutionGrant(ctx, grant), &run))
}

func TestInitialApplyAfterVerifiedDeletionKeepsOldHistory(t *testing.T) {
	for _, backend := range []string{"file", "sqlite"} {
		for _, scenario := range []string{"deleted", "missing-receipt", "newer-history", "old-active"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				cfg := config.DefaultServiceConfig()
				cfg.Paths.RunsDir = t.TempDir()
				cfg.Paths.WorkspacesDir = t.TempDir()
				cfg.State.Backend = ""
				if backend == "sqlite" {
					cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "state.db")
				}
				env, err := Open(cfg)
				require.NoError(t, err)
				defer env.Close()
				ctx := context.Background()
				repo, err := env.Repository("lab")
				require.NoError(t, err)
				st, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", st.TopologyUID))
				old := controlplane.Run{ID: "old", Topology: "lab", Op: "destroy", Revision: "old-revision", Status: controlplane.RunDone, AgentID: "host", QueuedAt: time.Now().Add(-time.Minute), EndedAt: time.Now().Add(-time.Second)}
				if scenario == "old-active" {
					old.Status = controlplane.RunRunning
				}
				require.NoError(t, env.Store.SaveRun(ctx, old))
				if scenario == "missing-receipt" {
					require.NoError(t, repo.StateBackend("lab").(state.DeleteBackend).Delete(ctx))
				} else {
					require.NoError(t, repo.DeleteQuiescentLifecycle(ctx, "lab", 1, st.TopologyUID, nil))
				}
				next, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.NotEqual(t, st.TopologyUID, next.TopologyUID)
				now := time.Now().UTC()
				run := controlplane.Run{ID: "new", Topology: "lab", Op: "apply", Revision: "new-revision", Status: controlplane.RunRunning, AgentID: "host", QueuedAt: now, StartedAt: now, Attempt: 1, LeaseOwner: "lease", LeaseUntil: now.Add(time.Minute)}
				if scenario == "newer-history" {
					old.ID = "intermediate"
					old.QueuedAt = now
					require.NoError(t, env.Store.SaveRun(ctx, old))
				}
				saveErr := env.Store.SaveRun(ctx, run)
				if scenario == "old-active" {
					require.Error(t, saveErr, "active old Run must block admitting a new lifecycle")
					return
				}
				require.NoError(t, saveErr)
				grant := initialApplyGrant(t, env, run, next.TopologyUID)
				err = env.initializeFirstApply(transport.WithParentExecutionGrant(ctx, grant), &run)
				if scenario == "deleted" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				historical, err := env.Store.GetRun(ctx, "old")
				require.NoError(t, err)
				require.Equal(t, old.Revision, historical.Revision)
			})
		}
	}
}
