package api

import (
	"context"
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
