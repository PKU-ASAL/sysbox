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

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.listAgents(r.Context())
	scrubAgentSecretValues(agents)
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	var req controlplane.Agent
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode agent: %w", err))
		return
	}
	agent, err := normalizeAgent(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.saveAgent(r.Context(), agent); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	scrubAgentSecret(&agent)
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	s.handleGetAgentByID(w, r.PathValue("agent"))
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent")
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent, err := s.getAgent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req struct {
		Disabled    *bool  `json:"disabled"`
		Quarantined *bool  `json:"quarantined"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode agent update: %w", err))
		return
	}
	if req.Disabled != nil {
		agent.Disabled = *req.Disabled
	}
	if req.Quarantined != nil {
		agent.Quarantined = *req.Quarantined
	}
	if req.Reason != "" {
		agent.Reason = req.Reason
	}
	switch {
	case agent.Disabled:
		agent.Status = "disabled"
	case agent.Quarantined:
		agent.Status = "quarantined"
	default:
		agent.Status = "online"
		agent.Reason = ""
	}
	agent.UpdatedAt = time.Now().UTC()
	if err := s.saveAgent(r.Context(), *agent); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	scrubAgentSecret(agent)
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleGetAgentByID(w http.ResponseWriter, id string) {
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if id == DefaultAgentID {
		agent := localAgent()
		scrubAgentSecret(&agent)
		writeJSON(w, http.StatusOK, agent)
		return
	}
	agent, err := s.getAgent(context.Background(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	scrubAgentSecret(agent)
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleClaimAgentRun(w http.ResponseWriter, r *http.Request) {
	s.handleClaimAgentRunByID(w, r.PathValue("agent"), r.PathValue("id"))
}

func (s *Server) handleClaimAgentRunByID(w http.ResponseWriter, agentID, runID string) {
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(runID, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.jobs.claim(runID, agentID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRenewAgentRun(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	runID := r.PathValue("id")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := validatePathSegment(runID, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		LeaseOwner string `json:"lease_owner"`
		TTLSeconds int    `json:"ttl_seconds,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode run renew: %w", err))
		return
	}
	if req.LeaseOwner == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("lease_owner is required"))
		return
	}
	ttl := 30 * time.Minute
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	run, err := s.jobs.renewLease(runID, agentID, req.LeaseOwner, ttl)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleCompleteNodeOperation(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	opID := r.PathValue("operation")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := validatePathSegment(opID, "operation"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var op controlplane.NodeOperation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode node operation: %w", err))
		return
	}
	if op.ID == "" {
		op.ID = opID
	}
	if op.ID != opID {
		writeError(w, http.StatusBadRequest, fmt.Errorf("operation id mismatch"))
		return
	}
	if op.AgentID == "" {
		op.AgentID = agentID
	}
	if op.AgentID != agentID {
		writeError(w, http.StatusBadRequest, fmt.Errorf("operation agent id mismatch"))
		return
	}
	if op.EndedAt.IsZero() && (op.Status == "done" || op.Status == "failed") {
		op.EndedAt = time.Now().UTC()
	}
	s.nodeOps.Save(op)
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleCompleteAgentRun(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	runID := r.PathValue("id")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := validatePathSegment(runID, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req controlplane.RunCompletion
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode run completion: %w", err))
		return
	}
	if req.Run.ID == "" {
		req.Run.ID = runID
	}
	if req.Run.ID != runID {
		writeError(w, http.StatusBadRequest, fmt.Errorf("completion run id mismatch"))
		return
	}
	if req.Run.AgentID == "" {
		req.Run.AgentID = agentID
	}
	if req.Run.AgentID != agentID {
		writeError(w, http.StatusBadRequest, fmt.Errorf("completion agent id mismatch"))
		return
	}
	s.jobs.replace(&req.Run)
	if req.Projection.AgentID == "" {
		req.Projection.AgentID = agentID
	}
	if req.Projection.Topology == "" {
		req.Projection.Topology = req.Run.Topology
	}
	if req.Projection.Workspace == "" {
		req.Projection.Workspace = req.Run.Workspace
	}
	if req.Projection.UpdatedAt.IsZero() {
		req.Projection.UpdatedAt = time.Now().UTC()
	}
	s.agents.SaveProjection(req.Projection)
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleListAgentProjections(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projections": s.agents.ListProjections(agentID)})
}

func (s *Server) handleListAgentCommandEvents(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	events, err := s.apiStore.ListAgentCommandEvents(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(events) == 0 {
		events = s.agents.ListCommandEvents(agentID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleListAgentCommands(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	commands, err := s.apiStore.ListAgentCommands(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": commands})
}

func (s *Server) handleCancelAgentCommand(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	commandID := r.PathValue("command")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(commandID, "command"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cmd, err := s.findAgentCommand(r.Context(), agentID, commandID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if commandTerminal(cmd.Status) {
		writeJSON(w, http.StatusOK, cmd)
		return
	}
	cancelCmd, err := s.publishAgentCommand(r.Context(), agentID, controlplane.AgentCommand{
		Type: "cancel_command",
		Operation: controlplane.NodeOperation{
			ExternalID: commandID,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, cancelCmd)
}

func (s *Server) handlePostAgentResourceProjection(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var req controlplane.ResourceProjection
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode resource projection: %w", err))
		return
	}
	if req.AgentID == "" {
		req.AgentID = agentID
	}
	if req.AgentID != agentID {
		writeError(w, http.StatusBadRequest, fmt.Errorf("projection agent id mismatch"))
		return
	}
	if req.Topology == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("projection topology is required"))
		return
	}
	if req.Workspace == "" {
		req.Workspace = req.Topology
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now().UTC()
	}
	if len(req.Resources) == 0 {
		req.Resources = req.Health.Resources
	}
	s.agents.SaveResourceProjection(req)
	writeJSON(w, http.StatusAccepted, req)
}

func (s *Server) handlePostAgentInventory(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var inv controlplane.AgentInventory
	if err := json.NewDecoder(r.Body).Decode(&inv); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode inventory: %w", err))
		return
	}
	if inv.AgentID == "" {
		inv.AgentID = agentID
	}
	if inv.AgentID != agentID {
		writeError(w, http.StatusBadRequest, fmt.Errorf("inventory agent id mismatch"))
		return
	}
	if inv.ObservedAt.IsZero() {
		inv.ObservedAt = time.Now().UTC()
	}
	inv.Status = inventoryStatus(inv.ObservedAt)
	inv.Stale = inv.Status == "stale"
	if err := s.apiStore.SaveAgentInventory(r.Context(), inv); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, inv)
}

func (s *Server) handleGetAgentInventory(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inv, err := s.apiStore.GetAgentInventory(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	inv.Status = inventoryStatus(inv.ObservedAt)
	inv.Stale = inv.Status == "stale"
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) handleAgentCommandStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent")
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.agents == nil {
		s.agents = newAgentRegistry()
	}
	if err := s.verifyAgentRequest(r, id); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	agent, err := s.getAgent(r.Context(), id)
	if err != nil && id != DefaultAgentID {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if agent != nil && (agent.Disabled || agent.Quarantined || agent.Status == "disabled" || agent.Status == "quarantined") {
		writeError(w, http.StatusForbidden, fmt.Errorf("agent %q is %s", id, agent.Status))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	stream := s.agents.Stream(id)
	ch := stream.Subscribe()
	defer stream.Unsubscribe(ch)
	ctx := r.Context()
	s.pushPendingAgentCommands(ctx, id, conn)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.readAgentCommandEvents(ctx, id, conn)
	}()
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			var cmd controlplane.AgentCommand
			if err := json.Unmarshal([]byte(line), &cmd); err != nil {
				continue
			}
			if cmd.ID == "" {
				continue
			}
			var leased bool
			cmd, leased = s.acquireAgentCommandLease(ctx, id, cmd)
			if !leased {
				continue
			}
			cmd = deliverAgentCommand(cmd)
			if s.apiStore != nil {
				_ = s.apiStore.SaveAgentCommand(ctx, cmd)
			}
			raw, err := json.Marshal(cmd)
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
				return
			}
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

func (s *Server) readAgentCommandEvents(ctx context.Context, agentID string, conn *websocket.Conn) error {
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
		s.updateAgentCommandFromEvent(ctx, event)
		s.agents.SaveCommandEvent(event)
		if s.apiStore != nil {
			_ = s.apiStore.SaveAgentCommandEvent(ctx, event)
		}
		fmt.Printf("[agent:%s] command %s %s %s\n", event.AgentID, event.CommandID, event.Type, event.Status)
	}
}

func (s *Server) pushPendingAgentCommands(ctx context.Context, agentID string, conn *websocket.Conn) {
	if s.apiStore == nil {
		return
	}
	commands, err := s.apiStore.ListAgentCommands(ctx, agentID)
	if err != nil {
		return
	}
	for _, cmd := range commands {
		var ok bool
		cmd, ok = s.acquireAgentCommandLease(ctx, agentID, cmd)
		if !ok {
			continue
		}
		cmd = deliverAgentCommand(cmd)
		_ = s.apiStore.SaveAgentCommand(ctx, cmd)
		raw, err := json.Marshal(cmd)
		if err != nil {
			continue
		}
		_ = conn.Write(ctx, websocket.MessageText, raw)
	}
}

func (s *Server) updateAgentCommandFromEvent(ctx context.Context, event controlplane.AgentCommandEvent) {
	if s.apiStore == nil || event.CommandID == "" || event.AgentID == "" {
		return
	}
	cmd, err := s.findAgentCommand(ctx, event.AgentID, event.CommandID)
	if err != nil {
		return
	}
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	switch event.Status {
	case "ack":
		cmd.Status = "acked"
		cmd.AckedAt = now
	case "started":
		cmd.Status = "running"
	case "completed":
		cmd.Status = "completed"
		cmd.EndedAt = now
	case "failed", "denied", "cancelled":
		cmd.Status = event.Status
		cmd.Err = event.Error
		cmd.EndedAt = now
	}
	_ = s.apiStore.SaveAgentCommand(ctx, cmd)
}

func inventoryStatus(observed time.Time) string {
	if observed.IsZero() {
		return "unknown"
	}
	if time.Since(observed) > 2*time.Minute {
		return "stale"
	}
	return "fresh"
}

func (s *Server) findAgentCommand(ctx context.Context, agentID, commandID string) (controlplane.AgentCommand, error) {
	commands, err := s.apiStore.ListAgentCommands(ctx, agentID)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	for _, cmd := range commands {
		if cmd.ID == commandID {
			return cmd, nil
		}
	}
	return controlplane.AgentCommand{}, fmt.Errorf("agent command not found")
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.handleAgentHeartbeatByID(w, r, r.PathValue("agent"))
}

func (s *Server) handleAgentHeartbeatByID(w http.ResponseWriter, r *http.Request, id string) {
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, id); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var req controlplane.Agent
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode agent heartbeat: %w", err))
			return
		}
	}
	req.ID = id
	req.Status = "online"
	agent, err := normalizeAgent(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if existing, err := s.getAgent(r.Context(), id); err == nil && existing != nil {
		if existing.Disabled || existing.Quarantined {
			agent.Disabled = existing.Disabled
			agent.Quarantined = existing.Quarantined
			agent.Reason = existing.Reason
			if existing.Disabled {
				agent.Status = "disabled"
			} else {
				agent.Status = "quarantined"
			}
		}
		if agent.Name == "" {
			agent.Name = existing.Name
		}
		if len(agent.Capabilities) == 0 {
			agent.Capabilities = existing.Capabilities
		}
		if len(agent.Labels) == 0 {
			agent.Labels = existing.Labels
		}
		if agent.Version == "" {
			agent.Version = existing.Version
		}
		if agent.SecretHash == "" {
			agent.SecretHash = existing.SecretHash
		}
		if agent.AuthSecret == "" {
			agent.AuthSecret = existing.AuthSecret
		}
		agent.CreatedAt = existing.CreatedAt
	}
	agent.LastHeartbeat = time.Now().UTC()
	agent.UpdatedAt = agent.LastHeartbeat
	if err := s.saveAgent(r.Context(), agent); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	scrubAgentSecret(&agent)
	writeJSON(w, http.StatusOK, agent)
}

func normalizeAgent(in controlplane.Agent) (controlplane.Agent, error) {
	if in.ID == "" {
		in.ID = in.Name
	}
	if err := validatePathSegment(in.ID, "agent"); err != nil {
		return controlplane.Agent{}, err
	}
	now := time.Now().UTC()
	if in.Name == "" {
		in.Name = in.ID
	}
	if in.Status == "" {
		in.Status = "online"
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = now
	}
	if in.Status == "online" && in.LastHeartbeat.IsZero() {
		in.LastHeartbeat = now
	}
	if in.Protocol == "" {
		in.Protocol = controlplane.AgentProtocolVersion
	}
	if in.Protocol != controlplane.AgentProtocolVersion {
		return controlplane.Agent{}, fmt.Errorf("unsupported agent protocol %q", in.Protocol)
	}
	return in, nil
}

func ensureLocalAgent(agents []controlplane.Agent) []controlplane.Agent {
	for _, agent := range agents {
		if agent.ID == DefaultAgentID {
			return agents
		}
	}
	return append(agents, localAgent())
}

func scrubAgentSecret(agent *controlplane.Agent) {
	if agent != nil {
		agent.AuthSecret = ""
	}
}

func scrubAgentSecretValues(values []controlplane.Agent) {
	for i := range values {
		values[i].AuthSecret = ""
	}
}

func localAgent() controlplane.Agent {
	now := time.Now().UTC()
	return controlplane.Agent{
		ID:            DefaultAgentID,
		Name:          "local API agent",
		Status:        "online",
		Protocol:      controlplane.AgentProtocolVersion,
		Capabilities:  []string{"docker", "network", "firecracker", "libvirt"},
		Labels:        map[string]string{"execution": "in-process"},
		LastHeartbeat: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
