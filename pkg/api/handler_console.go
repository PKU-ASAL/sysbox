package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Server) handleCreateConsoleSession(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	node := r.PathValue("node")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(node, "node"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req controlplane.ConsoleRequest
	if r.Body != nil {
		limitBody(w, r)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode console request: %w", err))
			return
		}
	}
	if req.TTY == nil {
		v := true
		req.TTY = &v
	}
	if req.Cols == 0 {
		req.Cols = 120
	}
	if req.Rows == 0 {
		req.Rows = 32
	}
	required, err := requiredCapabilitiesForNode(s.hclFile(topology), node)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent, err := s.selectAgent(r.Context(), required)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	sess := s.consoles.Create(topology, node, agent.ID, req)
	if err := s.agents.PublishConsole(agent.ID, sess, req); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sess)
}

func (s *Server) handleGetConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	if err := validatePathSegment(id, "session"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sess, err := s.consoles.Snapshot(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleCancelConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	if err := validatePathSegment(id, "session"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.consoles.Cancel(id, "cancelled by api request"); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	sess, _ := s.consoles.Snapshot(id)
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleAttachConsoleSession(w http.ResponseWriter, r *http.Request) {
	s.attachConsolePeer(w, r, "browser")
}

func (s *Server) handleAgentAttachConsole(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessID := r.PathValue("session")
	st, err := s.consoles.Get(sessID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if st.session.AgentID != agentID {
		writeError(w, http.StatusConflict, fmt.Errorf("session assigned to agent %q", st.session.AgentID))
		return
	}
	s.attachConsolePeer(w, r, "agent")
}

func (s *Server) attachConsolePeer(w http.ResponseWriter, r *http.Request, side string) {
	id := r.PathValue("session")
	if err := validatePathSegment(id, "session"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.consoles.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	peer := &wsPeer{conn: conn}
	if side == "agent" {
		s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
			sess.Status = "running"
			if sess.StartedAt.IsZero() {
				sess.StartedAt = time.Now().UTC()
			}
		})
		select {
		case st.agent <- peer:
		default:
			_ = conn.Close(websocket.StatusPolicyViolation, "agent already attached")
		}
		return
	}
	select {
	case st.browser <- peer:
	default:
		_ = conn.Close(websocket.StatusPolicyViolation, "browser already attached")
		return
	}
	go s.relayConsoleWhenReady(id, st)
}

func (s *Server) relayConsoleWhenReady(id string, st *consoleSessionState) {
	var browser, agent *wsPeer
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for browser == nil || agent == nil {
		select {
		case browser = <-st.browser:
		case agent = <-st.agent:
		case <-timer.C:
			s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
				sess.Status = "failed"
				sess.Err = "console peer attach timeout"
				sess.EndedAt = time.Now().UTC()
			})
			if browser != nil {
				_ = browser.conn.Close(websocket.StatusPolicyViolation, "agent attach timeout")
			}
			if agent != nil {
				_ = agent.conn.Close(websocket.StatusPolicyViolation, "browser attach timeout")
			}
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if st.request.TimeoutSeconds > 0 {
		go func() {
			select {
			case <-time.After(time.Duration(st.request.TimeoutSeconds) * time.Second):
				_ = s.consoles.Cancel(id, "console session timed out")
			case <-ctx.Done():
			}
		}()
	}
	done := make(chan struct{}, 2)
	go relayWebSocket(ctx, browser.conn, agent.conn, done)
	go relayWebSocket(ctx, agent.conn, browser.conn, done)
	select {
	case <-done:
	case <-st.cancel:
	}
	cancel()
	_ = browser.conn.Close(websocket.StatusNormalClosure, "")
	_ = agent.conn.Close(websocket.StatusNormalClosure, "")
	s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
		if sess.Status != "failed" {
			sess.Status = "closed"
		}
		sess.EndedAt = time.Now().UTC()
	})
}

func relayWebSocket(ctx context.Context, src, dst *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}
