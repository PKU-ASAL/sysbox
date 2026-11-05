package hostexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestFirstApplyInitializesBeforePreflightButNeverReinitializesHistory(t *testing.T) {
	for _, scenario := range []string{"fresh", "history", "destroy", "missing grant", "wrong owner"} {
		t.Run(scenario, func(t *testing.T) {
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
			run := controlplane.Run{ID: "apply", Topology: "lab", Op: "apply", Revision: "revision", Status: controlplane.RunRunning, AgentID: "host", QueuedAt: now, StartedAt: now, Attempt: 1, LeaseOwner: "lease", LeaseUntil: now.Add(time.Minute)}
			if scenario == "destroy" {
				run.Op = "destroy"
			}
			require.NoError(t, env.Store.SaveRun(ctx, run))
			if scenario == "history" {
				old := run
				old.ID = "old"
				old.Status = controlplane.RunDone
				old.QueuedAt = now.Add(-time.Hour)
				require.NoError(t, env.Store.SaveRun(ctx, old))
			}
			grant := initialApplyGrant(t, env, run, st.TopologyUID)
			if scenario == "wrong owner" {
				grant.Grant.Scope.TopologyUID = "lost-owner"
			}
			if scenario != "missing grant" {
				ctx = transport.WithParentExecutionGrant(ctx, grant)
			}
			err = env.initializeFirstApply(ctx, &run)
			if scenario == "fresh" || scenario == "destroy" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			after, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			if scenario == "fresh" {
				require.EqualValues(t, 1, after.Meta.Serial)
			} else {
				require.Zero(t, after.Meta.Serial)
			}
			destroy := run
			destroy.Op = "destroy"
			destroy.ID = "destroy"
			_, err = repo.ProveCleanup(ctx, destroy)
			if scenario == "fresh" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, persistence.ErrConflict)
			}
		})
	}
}

func TestFirstApplyPreflightFailureThenDestroyUsesNormalReceipt(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte("invalid HCL !"), 0600))
	now := time.Now().UTC()
	run := controlplane.Run{ID: "first", Topology: "lab", Op: "apply", Revision: "revision", Status: controlplane.RunRunning, AgentID: "host", QueuedAt: now, StartedAt: now, Attempt: 1, LeaseOwner: "lease", LeaseUntil: now.Add(time.Minute)}
	require.NoError(t, env.Store.SaveRun(ctx, run))
	grant := initialApplyGrant(t, env, run, st.TopologyUID)
	runCtx := WithRunWorkspace(transport.WithParentExecutionGrant(ctx, grant), run, path)
	NewExecutorWithBridge(env).ExecuteContext(runCtx, &run)
	require.Equal(t, controlplane.RunFailed, run.Status)
	require.Contains(t, run.Err, "field.sysbox.hcl")
	after, actions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	require.EqualValues(t, 1, after.Meta.Serial)
	require.Empty(t, actions)
	require.Empty(t, after.Resources)
	require.NoError(t, env.Store.SaveRun(ctx, run))
	destroy := controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy", Revision: "revision", Status: controlplane.RunRunning, AgentID: "host", QueuedAt: time.Now().UTC(), StartedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "cleanup", LeaseUntil: time.Now().Add(time.Minute)}
	require.NoError(t, env.Store.SaveRun(ctx, destroy))
	NewExecutorWithBridge(env).ExecuteContext(ctx, &destroy)
	require.Equal(t, controlplane.RunDone, destroy.Status, destroy.Err)
	require.NotNil(t, destroy.Cleanup)
	require.Equal(t, after.TopologyUID, destroy.Cleanup.TopologyUID)
	require.True(t, destroy.Cleanup.Empty)
}

func initialApplyGrant(t *testing.T, env *Environment, run controlplane.Run, uid string) controlplane.SignedExecutionGrant {
	t.Helper()
	ctx := context.Background()
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: env.Store})
	session, err := authority.OpenSession(ctx, run.AgentID, "boot", "")
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: run.AgentID, SessionID: session.ID, ActionID: run.ID, CommandID: "command-" + run.ID, Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "host", Scope: controlplane.ExecutionScope{TopologyUID: uid}}, time.Minute)
	require.NoError(t, err)
	return controlplane.SignedExecutionGrant{Grant: *grant, BootID: "host"}
}

func TestFirstApplyTransactionKeepsAuthorityAndHistoryBound(t *testing.T) {
	for _, backend := range []string{"file", "sqlite"} {
		for _, scenario := range []string{"fresh", "revoked", "maintenance", "changed-claim", "missing-history", "old-incarnation", "queued-history"} {
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
				now := time.Now().UTC()
				run := controlplane.Run{ID: "first", Topology: "lab", Op: "apply", Revision: "revision", Status: controlplane.RunRunning, AgentID: "host", QueuedAt: now, StartedAt: now, Attempt: 1, LeaseOwner: "lease", LeaseUntil: now.Add(time.Minute)}
				if scenario == "queued-history" {
					queued := run
					queued.Status = controlplane.RunQueued
					require.NoError(t, env.Store.SaveRun(ctx, queued))
				}
				stored := run
				if scenario == "changed-claim" {
					stored.LeaseOwner = "new-claim"
				}
				if scenario == "old-incarnation" {
					stored.QueuedAt = now.Add(-time.Minute)
				}
				if scenario != "missing-history" {
					require.NoError(t, env.Store.SaveRun(ctx, stored))
				}
				grant := initialApplyGrant(t, env, run, st.TopologyUID)
				authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: env.Store})
				if scenario == "revoked" {
					require.NoError(t, authority.Revoke(ctx, grant.Grant))
				}
				if scenario == "maintenance" {
					_, err = authority.BeginMaintenance(ctx, "maintenance", st.TopologyUID, "digest")
					require.NoError(t, err)
				}
				ctx = transport.WithParentExecutionGrant(ctx, grant)
				err = env.initializeFirstApply(ctx, &run)
				success := scenario == "fresh" || scenario == "queued-history"
				if success {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				after, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				if success {
					require.EqualValues(t, 1, after.Meta.Serial)
				} else {
					require.Zero(t, after.Meta.Serial)
				}
			})
		}
	}
}
