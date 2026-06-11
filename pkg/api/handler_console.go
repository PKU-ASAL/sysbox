package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/controlplane"
	"io"
	"net/http"
	"time"
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
	defaultTimeout, _ := time.ParseDuration(s.cfg.API.Console.DefaultTimeout)
	maxTimeout, _ := time.ParseDuration(s.cfg.API.Console.MaxTimeout)
	if defaultTimeout <= 0 {
		defaultTimeout = time.Hour
	}
	if maxTimeout <= 0 {
		maxTimeout = 24 * time.Hour
	}
	if req.TimeoutSeconds == 0 && defaultTimeout > 0 {
		req.TimeoutSeconds = int(defaultTimeout.Seconds())
	}
	if req.TimeoutSeconds < 0 || time.Duration(req.TimeoutSeconds)*time.Second > maxTimeout {
		writeError(w, http.StatusBadRequest, fmt.Errorf("timeout_seconds must be between 0 and %d", int(maxTimeout.Seconds())))
		return
	}
	subj := s.requestSubject(r)
	if req.RequestedBy == "" {
		req.RequestedBy = subj.User
	}
	if len(req.Roles) == 0 {
		req.Roles = subj.Roles
	}
	req.Policy = "console.rbac"
	required, err := requiredCapabilitiesForNode(s.hclFile(topology), node)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent, err := s.selectAgent(r.Context(), required, "")
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	sess := s.consoles.Create(topology, node, agent.ID, req)
	if err := s.authorizeConsole(requestSubject{User: req.RequestedBy, Roles: req.Roles}, sess); err != nil {
		s.consoles.Update(sess.ID, func(sess *controlplane.ConsoleSession) {
			sess.Status = "denied"
			sess.Err = err.Error()
			sess.EndedAt = time.Now().UTC()
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "deny", err.Error()))
		})
		got, _ := s.consoles.Snapshot(sess.ID)
		writeJSON(w, http.StatusForbidden, got)
		return
	}
	s.consoles.Update(sess.ID, func(sess *controlplane.ConsoleSession) {
		sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "allow", "console session allowed by policy"))
	})
	sess, _ = s.consoles.Snapshot(sess.ID)
	if err := s.ensureConsoleNodeRunning(r.Context(), topology, node); err != nil {
		s.consoles.Update(sess.ID, func(sess *controlplane.ConsoleSession) {
			sess.Status = "failed"
			sess.Err = err.Error()
			sess.EndedAt = time.Now().UTC()
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "reject", err.Error()))
		})
		writeError(w, http.StatusConflict, err)
		return
	}
	if _, err := s.publishAgentCommand(r.Context(), agent.ID, controlplane.AgentCommand{
		Type:    "session_open",
		Session: &sess,
		Request: req,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sess)
}

func (s *Server) ensureConsoleNodeRunning(ctx context.Context, topology, node string) error {
	st, err := s.loadState(topology)
	if err != nil {
		return err
	}
	health := s.authoritativeTopologyHealth(ctx, topology, st)
	resourceID := "sysbox_node." + node
	for _, resource := range health.Resources {
		if resource.Resource != resourceID {
			continue
		}
		if resource.Status != "healthy" {
			return fmt.Errorf("node %q is %s; repair required before opening a console", node, consoleHealthReason(resource))
		}
		if resource.Observation != nil && !resource.Observation.Running {
			return fmt.Errorf("node %q is %s; repair required before opening a console", node, consoleHealthReason(resource))
		}
		return nil
	}
	return fmt.Errorf("node %q has no health observation; repair required before opening a console", node)
}

func consoleHealthReason(resource controlplane.ResourceHealth) string {
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
	subj := s.requestSubject(r)
	if err := s.consoles.Cancel(id, "cancelled by api request", subj.User); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	sess, _ := s.consoles.Snapshot(id)
	_, _ = s.publishAgentCommand(r.Context(), sess.AgentID, controlplane.AgentCommand{
		Type: "cancel_command",
		Session: &controlplane.ConsoleSession{
			ID: sess.ID,
		},
	})
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
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
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
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
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
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "attach", "agent attached"))
		})
		select {
		case st.agent <- peer:
			<-st.done
		default:
			_ = conn.Close(websocket.StatusPolicyViolation, "agent already attached")
		}
		return
	}
	select {
	case st.browser <- peer:
		s.consoles.Update(id, func(sess *controlplane.ConsoleSession) {
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "attach", "browser attached"))
		})
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
				_ = s.consoles.Cancel(id, "console session timed out", "system")
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
