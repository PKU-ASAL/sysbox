package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRepublishPendingCommandsRebuildsWakeupAfterRestart(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "restart-command", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusQueued}))
	registry := newAgentRegistry()
	service := &AgentService{store: store, registry: registry}
	stream := registry.Stream("agent")
	ch := stream.Subscribe()
	defer stream.Unsubscribe(ch)
	require.NoError(t, service.RepublishPendingCommands(ctx))
	select {
	case raw := <-ch:
		require.Contains(t, raw, "restart-command")
	case <-time.After(time.Second):
		t.Fatal("pending command was not republished")
	}
}

func TestRepublishPendingCommandsBacksOffWakeupFailure(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "retry-command", AgentID: "agent", Type: "node_operation", Status: "queued"}))
	service := &AgentService{store: store, registry: newAgentRegistry(), dispatchRetry: map[string]dispatchRetryState{}, wakeup: func(string, controlplane.AgentCommand) error { return errors.New("stream unavailable") }}
	require.NoError(t, service.RepublishPendingCommands(ctx))
	failures, next, last := service.DispatchRetryState("retry-command")
	require.Equal(t, 1, failures)
	require.False(t, next.IsZero())
	require.Equal(t, "stream unavailable", last)
	require.NoError(t, service.RepublishPendingCommands(ctx))
	failures2, _, _ := service.DispatchRetryState("retry-command")
	require.Equal(t, failures, failures2)
}
