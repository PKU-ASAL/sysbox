package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestAgentCommandStreamRejectsForeignEventIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := &localAPIStore{runsDir: t.TempDir()}
	cmd := controlplane.AgentCommand{ID: "foreign-command", AgentID: "victim", Type: "node_operation", Status: controlplane.AgentCommandStatusQueued}
	require.NoError(t, store.SaveAgentCommand(ctx, cmd))
	service := AgentStreamService{agentService: &AgentService{store: store}}
	finished := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			finished <- err
			return
		}
		defer conn.CloseNow()
		finished <- service.readCommandEvents(ctx, "authenticated", conn)
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	raw, err := json.Marshal(controlplane.AgentCommandEvent{AgentID: "victim", CommandID: cmd.ID, Status: "ack"})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, raw))
	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("stream did not stop")
	}
	got, err := store.ListAgentCommands(ctx, "victim")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, controlplane.AgentCommandStatusQueued, got[0].Status)
}
func TestAgentCommandLeaseCannotFallBackToMemory(t *testing.T) {
	service := AgentService{}
	_, ok := service.AcquireCommandLease(context.Background(), "agent", controlplane.AgentCommand{ID: "command", AgentID: "agent", Status: controlplane.AgentCommandStatusQueued})
	require.False(t, ok, "delivery requires a durable claim")
}
