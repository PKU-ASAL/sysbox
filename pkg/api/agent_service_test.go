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
	svc, _, registry, _ := newTestAgentService(t)
	ctx := context.Background()
	cmd, err := svc.PublishCommand(ctx, "host-a", controlplane.AgentCommand{Type: "node_operation"})
	require.NoError(t, err)

	now := time.Now().UTC()
	svc.RecordCommandEvent(ctx, controlplane.AgentCommandEvent{
		AgentID:   "host-a",
		CommandID: cmd.ID,
		Status:    "ack",
		CreatedAt: now,
	})

	got, err := svc.FindCommand(ctx, "host-a", cmd.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.AgentCommandStatusAcknowledged, got.Status)

	// A terminal event removes the command rather than keeping a completed row.
	svc.RecordCommandEvent(ctx, controlplane.AgentCommandEvent{
		AgentID:   "host-a",
		CommandID: cmd.ID,
		Status:    controlplane.AgentCommandStatusCompleted,
		CreatedAt: now,
	})
	_, err = svc.FindCommand(ctx, "host-a", cmd.ID)
	require.Error(t, err)

	require.Len(t, registry.ListCommandEvents("host-a"), 2)
}

func TestAgentServiceReconcilesStalePendingCommandFromGuestOperation(t *testing.T) {
	svc, store, _, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	execution := controlplane.GuestExecution{ID: "exec-1", CommandID: "cmd-1", Version: 1, AgentID: "host-a", Topology: "lab", Node: "web", Status: controlplane.GuestExecutionCompleted, CreatedAt: now, EndedAt: now}
	require.NoError(t, store.SaveGuestExecution(ctx, execution))
	command := controlplane.AgentCommand{ID: "cmd-1", AgentID: "host-a", Type: "guest_execution", Status: controlplane.AgentCommandStatusDelivered, Execution: &execution, CreatedAt: now}
	require.NoError(t, store.SaveAgentCommand(ctx, command))

	reconciled, terminal := svc.ReconcileCommandState(ctx, command)
	require.True(t, terminal)
	require.Equal(t, controlplane.AgentCommandStatusCompleted, reconciled.Status)
	require.False(t, reconciled.EndedAt.IsZero())

	stored, err := svc.FindCommand(ctx, "host-a", "cmd-1")
	require.NoError(t, err)
	require.Equal(t, controlplane.AgentCommandStatusCompleted, stored.Status)
}

func TestAgentServiceLeavesLiveGuestCommandPending(t *testing.T) {
	svc, store, _, _ := newTestAgentService(t)
	ctx := context.Background()
	execution := controlplane.GuestExecution{ID: "exec-live", CommandID: "cmd-live", Version: 1, AgentID: "host-a", Topology: "lab", Node: "web", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
	require.NoError(t, store.SaveGuestExecution(ctx, execution))
	command := controlplane.AgentCommand{ID: "cmd-live", AgentID: "host-a", Type: "guest_execution", Status: controlplane.AgentCommandStatusDelivered, Execution: &execution}

	reconciled, terminal := svc.ReconcileCommandState(ctx, command)
	require.False(t, terminal)
	require.Equal(t, controlplane.AgentCommandStatusDelivered, reconciled.Status)
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
