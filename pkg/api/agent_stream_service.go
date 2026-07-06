package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type AgentStreamService struct {
	agentService  *AgentService
	store         apiStore
	registry      *agentRegistry
	originPattern func() []string
	verifyAgent   func(*http.Request, string) error
}

func newAgentStreamService(server *Server) *AgentStreamService {
	return &AgentStreamService{
		agentService:  server.agentService(),
		store:         server.apiStore,
		registry:      server.agents,
		originPattern: server.originPatterns,
		verifyAgent:   server.verifyAgentRequest,
	}
}

func (s *AgentStreamService) ServeCommands(w http.ResponseWriter, r *http.Request, agentID string) {
	if err := s.verifyAgent(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	agent, err := s.agentService.Get(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if agent != nil && agent.IsBlocked() {
		writeError(w, http.StatusForbidden, fmt.Errorf("agent %q is %s", agentID, agent.Status))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPattern()})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	s.runCommandLoop(r.Context(), agentID, conn)
}

func (s *AgentStreamService) runCommandLoop(ctx context.Context, agentID string, conn *websocket.Conn) {
	stream := s.registry.Stream(agentID)
	ch := stream.Subscribe()
	defer stream.Unsubscribe(ch)
	s.pushPendingCommands(ctx, agentID, conn)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.readCommandEvents(ctx, agentID, conn)
	}()
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			s.deliverCommandLine(ctx, agentID, conn, line)
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				return
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *AgentStreamService) deliverCommandLine(ctx context.Context, agentID string, conn *websocket.Conn, line string) {
	var cmd controlplane.AgentCommand
	if err := json.Unmarshal([]byte(line), &cmd); err != nil || cmd.ID == "" {
		return
	}
	var leased bool
	cmd, leased = s.agentService.AcquireCommandLease(ctx, agentID, cmd)
	if !leased {
		return
	}
	_ = s.writeCommand(ctx, conn, deliverAgentCommand(cmd))
}

func (s *AgentStreamService) readCommandEvents(ctx context.Context, agentID string, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var event controlplane.AgentCommandEvent
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		if event.AgentID == "" {
			event.AgentID = agentID
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		s.agentService.RecordCommandEvent(ctx, event)
		fmt.Printf("[agent:%s] command %s %s %s\n", event.AgentID, event.CommandID, event.Type, event.Status)
	}
}

func (s *AgentStreamService) pushPendingCommands(ctx context.Context, agentID string, conn *websocket.Conn) {
	if s.store == nil {
		return
	}
	commands, err := s.store.ListAgentCommands(ctx, agentID)
	if err != nil {
		return
	}
	for _, cmd := range commands {
		var ok bool
		cmd, ok = s.agentService.AcquireCommandLease(ctx, agentID, cmd)
		if !ok {
			continue
		}
		_ = s.writeCommand(ctx, conn, deliverAgentCommand(cmd))
	}
}

func (s *AgentStreamService) writeCommand(ctx context.Context, conn *websocket.Conn, cmd controlplane.AgentCommand) error {
	if s.store != nil {
		_ = s.store.SaveAgentCommand(ctx, cmd)
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}
