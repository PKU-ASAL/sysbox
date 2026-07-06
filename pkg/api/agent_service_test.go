package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func newTestAgentService(t *testing.T) (*AgentService, apiStore, *agentRegistry, *Jobs) {
	t.Helper()
	runsDir := t.TempDir()
	store := &localAPIStore{runsDir: runsDir}
	registry := newAgentRegistry()
	jobs := newJobsWithRecovery(runsDir, store, false)
	consoles := newConsoleSessionHub(store)
	svc := &AgentService{
		store:           store,
		registry:        registry,
		jobs:            jobs,
		consoles:        consoles,
		commandTTL:      func() time.Duration { return time.Minute },
		agentOfflineTTL: func() time.Duration { return time.Minute },
	}
	return svc, store, registry, jobs
}

func TestAgentServicePublishAndAcquireCommandLease(t *testing.T) {
	svc, store, _, _ := newTestAgentService(t)
	ctx := context.Background()

	cmd, err := svc.PublishCommand(ctx, "host-a", controlplane.AgentCommand{Type: "node_operation"})
	require.NoError(t, err)
	require.NotEmpty(t, cmd.ID)
	require.Equal(t, "host-a", cmd.AgentID)
	require.Equal(t, controlplane.AgentCommandStatusQueued, cmd.Status)
	require.Equal(t, controlplane.AgentProtocolVersion, cmd.Protocol)

	leased, ok := svc.AcquireCommandLease(ctx, "host-a", cmd)
	require.True(t, ok)
	require.Equal(t, "leased", leased.Status)
	require.NotEmpty(t, leased.LeaseOwner)
	require.False(t, leased.LeaseUntil.IsZero())
	require.Equal(t, 1, leased.Attempt)

	stored, err := store.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, leased.Status, stored[0].Status)
}

func TestAgentServiceRecordCommandEventUpdatesCommand(t *testing.T) {
	svc, store, registry, _ := newTestAgentService(t)
	ctx := context.Background()
	cmd, err := svc.PublishCommand(ctx, "host-a", controlplane.AgentCommand{Type: "node_operation"})
	require.NoError(t, err)

	now := time.Now().UTC()
	svc.RecordCommandEvent(ctx, controlplane.AgentCommandEvent{
		AgentID:   "host-a",
		CommandID: cmd.ID,
		Status:    controlplane.AgentCommandStatusCompleted,
		CreatedAt: now,
	})

	got, err := svc.FindCommand(ctx, "host-a", cmd.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.AgentCommandStatusCompleted, got.Status)
	require.Equal(t, now, got.EndedAt)

	events, err := store.ListAgentCommandEvents(ctx, "host-a")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Len(t, registry.ListCommandEvents("host-a"), 1)
}

func TestAgentServiceCompleteRunStoresProjection(t *testing.T) {
	svc, _, registry, jobs := newTestAgentService(t)
	run := jobs.start("mixed", "apply")

	completion, err := svc.CompleteRun("host-a", run.ID, controlplane.RunCompletion{
		Run: controlplane.Run{
			ID:        run.ID,
			AgentID:   "host-a",
			Topology:  "mixed",
			Workspace: "mixed",
			Status:    controlplane.RunDone,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "host-a", completion.Projection.AgentID)
	require.Equal(t, "mixed", completion.Projection.Topology)
	require.Equal(t, "mixed", completion.Projection.Workspace)

	got, ok := jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunDone, got.Status)

	projections := registry.ListProjections("host-a")
	require.Len(t, projections, 1)
	require.Equal(t, "mixed", projections[0].Topology)
}
