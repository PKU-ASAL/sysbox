package agentexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestCommandWebSocketAcceptsLargeAgentCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept command stream: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		cmd := controlplane.AgentCommand{
			ID:   "large-command",
			Type: "guest_execution",
			ExecutionRequest: controlplane.GuestExecutionRequest{
				Argv: []string{strings.Repeat("x", 64<<10)},
			},
		}
		payload, err := json.Marshal(cmd)
		if err != nil {
			t.Errorf("encode command: %v", err)
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Errorf("write large command: %v", err)
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial command stream: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	data, err := readAgentCommandMessage(ctx, conn)
	if err != nil {
		t.Fatalf("read large command: %v", err)
	}
	if len(data) <= 32<<10 {
		t.Fatalf("test message was not larger than the old read limit: %d bytes", len(data))
	}
	var cmd controlplane.AgentCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		t.Fatalf("decode large command: %v", err)
	}
	if cmd.ID != "large-command" || len(cmd.ExecutionRequest.Argv[0]) != 64<<10 {
		t.Fatalf("large command was not preserved: id=%q argv_length=%d", cmd.ID, len(cmd.ExecutionRequest.Argv[0]))
	}
}
