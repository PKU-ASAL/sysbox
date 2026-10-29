package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
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
	result, err := s.consoleService().CreateSession(r.Context(), topology, node, req, s.requestSubject(r))
	if err != nil {
		if result.Status == http.StatusServiceUnavailable {
			writeError(w, result.Status, errors.New("console session persistence unavailable"))
			return
		}
		if result.Session.ID != "" {
			writeJSON(w, result.Status, result.Session)
			return
		}
		writeError(w, result.Status, err)
		return
	}
	writeJSON(w, result.Status, result.Session)
}

func consoleHealthReason(resource model.ResourceHealth) string {
	if resource.Reason != "" {
		return resource.Reason
	}
	if resource.Observation != nil && resource.Observation.Status != "" {
		return string(resource.Observation.Status)
	}
	if resource.Status != "" {
		return string(resource.Status)
	}
	return "unknown"
}

func (s *Server) handleGetConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	if err := validatePathSegment(id, "session"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sess, err := s.consoleService().Get(id, s.requestSubject(r))
	if err != nil {
		writeConsoleSessionError(w, err)
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
	sess, err := s.consoleService().Cancel(r.Context(), id, s.requestSubject(r))
	if err != nil {
		writeConsoleSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func writeConsoleSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errConsoleAccessDenied):
		writeError(w, http.StatusForbidden, errConsoleAccessDenied)
	case errors.Is(err, errConsoleSessionNotFound):
		writeError(w, http.StatusNotFound, errConsoleSessionNotFound)
	default:
		writeError(w, http.StatusServiceUnavailable, errors.New("console session unavailable; retry the same session"))
	}
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
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	sessID := r.PathValue("session")
	sess, err := s.consoles.Snapshot(sessID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if sess.AgentID != agentID {
		writeError(w, http.StatusConflict, fmt.Errorf("session assigned to agent %q", sess.AgentID))
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
	if side == "browser" {
		if _, err := s.consoleService().Get(id, s.requestSubject(r)); err != nil {
			writeConsoleSessionError(w, err)
			return
		}
	}
	st, err := s.consoles.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
	if err != nil {
		return
	}
	peer := &wsPeer{conn: conn}
	if side == "agent" {
		if err := s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
			if sess.Status == controlplane.ConsoleSessionStatusQueued {
				sess.Status = controlplane.ConsoleSessionStatusRunning
			}
			if sess.StartedAt.IsZero() {
				sess.StartedAt = time.Now().UTC()
			}
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "attach", "agent attached"))
		}); err != nil {
			_ = conn.Close(websocket.StatusInternalError, "console persistence unavailable")
			return
		}
		select {
		case st.agent <- peer:
			<-st.done
		default:
			_ = conn.Close(websocket.StatusPolicyViolation, "agent already attached")
		}
		return
	}
	if err := s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
		// The channel admission happens only after persistence. This records
		// the request, including duplicates, not a successful attachment.
		sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "attach_requested", "browser attachment requested"))
	}); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "console persistence unavailable")
		return
	}
	select {
	case st.browser <- peer:
	default:
		_ = conn.Close(websocket.StatusPolicyViolation, "browser already attached")
		return
	}
	go s.relayConsoleWhenReady(id, st)
	<-st.done
}

func (s *Server) relayConsoleWhenReady(id string, st *consoleSessionState) {
	defer closeConsoleSessionDone(st)
	var browser, agent *wsPeer
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for browser == nil || agent == nil {
		select {
		case browser = <-st.browser:
		case agent = <-st.agent:
		case <-timer.C:
			if err := s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
				recordConsoleTransportLoss(sess, "console peer attach timeout")
			}); err != nil {
				slog.Error("persist console transport loss", "session", id, "error", err)
			}
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
				if err := s.consoles.Cancel(ctx, id, "console session timed out", requestSubject{User: "system"}); err != nil {
					slog.Warn("persist console timeout cancellation", "session_id", id, "error", err)
				}
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
	if err := s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
		recordConsoleTransportLoss(sess, "console relay ended without execution completion")
	}); err != nil {
		slog.Error("persist console transport loss", "session", id, "error", err)
	}
}

func closeConsoleSessionDone(st *consoleSessionState) {
	select {
	case <-st.done:
	default:
		close(st.done)
	}
}

func consoleAuditEvent(sess controlplane.ConsoleSession, action, message string) controlplane.Event {
	return controlplane.Event{
		ProjectID: sess.ProjectID,
		Workspace: sess.Workspace,
		Resource:  "sysbox_node." + sess.Node,
		Action:    action,
		Status:    sess.Status,
		Actor:     sess.RequestedBy,
		Roles:     append([]string{}, sess.Roles...),
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
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
