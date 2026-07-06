package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

func TestLocalAPIStorePersistsRunCheckpointAndHealth(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()

	version, err := store.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, apiSchemaVersion, version)

	run := controlplane.Run{ID: "run-1", Topology: "mixed", Op: "apply", Status: controlplane.RunRunning, StartedAt: time.Now().UTC()}
	require.NoError(t, store.SaveRun(ctx, run))
	runs, err := store.LoadRuns(ctx)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "run-1", runs[0].ID)

	cp := runtime.OperationCheckpoint{RunID: "run-1", Topology: "mixed", Operation: "apply", Status: runtime.OperationStarted}
	require.NoError(t, store.SaveCheckpoint(ctx, "mixed", "run-1", cp))
	gotCP, err := store.LoadCheckpoint(ctx, "mixed", "run-1")
	require.NoError(t, err)
	require.Equal(t, "run-1", gotCP.RunID)

	snap := HealthSnapshot{Topology: "mixed", Observed: time.Now().UTC(), Policy: SupervisorPolicyObserveOnly}
	require.NoError(t, store.SaveHealth(ctx, "mixed", snap))
	gotHealth, err := store.LoadHealth(ctx, "mixed")
	require.NoError(t, err)
	require.Equal(t, "mixed", gotHealth.Topology)
}

func TestAPIMigrationsMatchSchemaVersion(t *testing.T) {
	require.NotEmpty(t, apiMigrations)
	seen := map[int]bool{}
	for i, migration := range apiMigrations {
		require.Equal(t, i+1, migration.Version)
		require.NotEmpty(t, migration.Name)
		require.NotEmpty(t, migration.SQL)
		require.False(t, seen[migration.Version])
		seen[migration.Version] = true
	}
	require.Equal(t, apiSchemaVersion, apiMigrations[len(apiMigrations)-1].Version)
}

func TestLocalAPIStorePersistsAgentAndClaimLease(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()

	agent := controlplane.Agent{ID: "host-a", Status: "online", Disabled: true, Capabilities: []string{"docker"}}
	require.NoError(t, store.SaveAgent(ctx, agent))
	gotAgent, err := store.GetAgent(ctx, "host-a")
	require.NoError(t, err)
	require.Equal(t, "host-a", gotAgent.ID)
	require.True(t, gotAgent.Disabled)
	require.Equal(t, controlplane.AgentProtocolVersion, gotAgent.Protocol)

	run := controlplane.Run{
		ID:         "run-1",
		Topology:   "mixed",
		Workspace:  "mixed",
		AgentID:    "host-a",
		Status:     controlplane.RunAssigned,
		QueuedAt:   time.Now().UTC(),
		AssignedAt: time.Now().UTC(),
		StartedAt:  time.Now().UTC(),
	}
	require.NoError(t, store.SaveRun(ctx, run))
	claimed, ok, err := store.ClaimRun(ctx, "run-1", "host-a", "owner-1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, claimed.Status)
	require.Equal(t, 1, claimed.Attempt)
	require.Equal(t, "owner-1", claimed.LeaseOwner)

	renewed, ok, err := store.RenewRunLease(ctx, "run-1", "host-a", "owner-1", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, renewed.LeaseUntil.After(claimed.LeaseUntil))

	_, ok, err = store.ClaimRun(ctx, "run-1", "host-a", "owner-2", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
}
