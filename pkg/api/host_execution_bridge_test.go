package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestHostExecutionBridgeFinishPreservesCanonicalRun(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		for _, outcome := range []string{"success", "failure"} {
			t.Run(backend+"/"+outcome, func(t *testing.T) {
				cfg := config.DefaultServiceConfig()
				cfg.Paths.RunsDir = t.TempDir()
				cfg.Paths.WorkspacesDir = t.TempDir()
				if backend == "sqlite" {
					cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "runs.db")
				}
				bridge, err := NewExecutionBridge(cfg)
				require.NoError(t, err)
				ctx := context.Background()
				run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
				require.NoError(t, bridge.store.SaveRun(ctx, run))
				claimed, ok, err := bridge.store.ClaimRun(ctx, run.ID, run.AgentID, "worker", time.Minute)
				require.NoError(t, err)
				require.True(t, ok)
				before, err := bridge.ParentRun(ctx, run.ID)
				require.NoError(t, err)
				claimed.Inputs = map[string]string{"password": "secret"}
				bridge.AttachRun(claimed)
				var finishErr error
				wantStatus := controlplane.RunDone
				if outcome == "failure" {
					finishErr = errors.New("provider failed")
					wantStatus = controlplane.RunFailed
				}
				bridge.Finish(claimed, finishErr)
				require.Equal(t, wantStatus, claimed.Status)
				require.False(t, claimed.EndedAt.IsZero())
				require.Nil(t, claimed.Inputs)
				if finishErr != nil {
					require.Equal(t, finishErr.Error(), claimed.Err)
				}
				after, err := bridge.ParentRun(ctx, run.ID)
				require.NoError(t, err)
				require.Equal(t, controlplane.RunRunning, after.Status)
				require.Equal(t, before, after, "host completion must await signed canonical completion")
			})
		}
	}
}

func TestNewExecutionBridgeRejectsInitializationFailure(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://"
	bridge, err := NewExecutionBridge(cfg)
	require.ErrorContains(t, err, "resolved durable filesystem path")
	require.Nil(t, bridge, "invalid canonical storage must not fall back to a local store")
}
