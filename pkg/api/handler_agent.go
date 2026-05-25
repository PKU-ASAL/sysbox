package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := ensureLocalAgent(s.agents.List())
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
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
	s.agents.Save(agent)
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	s.handleGetAgentByID(w, r.PathValue("agent"))
}

func (s *Server) handleGetAgentByID(w http.ResponseWriter, id string) {
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if id == DefaultAgentID {
		writeJSON(w, http.StatusOK, localAgent())
		return
	}
	agent, err := s.agents.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleListAgentRuns(w http.ResponseWriter, r *http.Request) {
	s.handleListAgentRunsByID(w, r.PathValue("agent"))
}

func (s *Server) handleListAgentRunsByID(w http.ResponseWriter, id string) {
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.assignedRunsForAgent(context.Background(), id)})
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

func (s *Server) handleCompleteAgentRun(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	runID := r.PathValue("id")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
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

func (s *Server) handlePostAgentResourceProjection(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
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

func (s *Server) handleAgentStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent")
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.agents == nil {
		s.agents = newAgentRegistry()
	}
	if _, err := s.agents.Get(id); err != nil && id != DefaultAgentID {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	stream := s.agents.Stream(id)
	ch := stream.Subscribe()
	defer stream.Unsubscribe(ch)
	ServeSSE(w, r, ch)
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.handleAgentHeartbeatByID(w, r, r.PathValue("agent"))
}

func (s *Server) handleAgentHeartbeatByID(w http.ResponseWriter, r *http.Request, id string) {
	if err := validatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	if existing, err := s.agents.Get(id); err == nil && existing != nil {
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
		agent.CreatedAt = existing.CreatedAt
	}
	agent.LastHeartbeat = time.Now().UTC()
	agent.UpdatedAt = agent.LastHeartbeat
	s.agents.Save(agent)
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
	return in, nil
}

func (s *Server) assignedRunsForAgent(ctx context.Context, agentID string) []Run {
	runs, err := s.apiStore.LoadRuns(ctx)
	if err != nil {
		return nil
	}
	out := make([]Run, 0)
	for _, run := range latestRunsByID(runs) {
		normalizeRunProductFields(&run)
		if run.AgentID == agentID && run.Status == RunAssigned {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QueuedAt.Before(out[j].QueuedAt) })
	return out
}

func ensureLocalAgent(agents []controlplane.Agent) []controlplane.Agent {
	for _, agent := range agents {
		if agent.ID == DefaultAgentID {
			return agents
		}
	}
	return append(agents, localAgent())
}

func localAgent() controlplane.Agent {
	now := time.Now().UTC()
	return controlplane.Agent{
		ID:            DefaultAgentID,
		Name:          "local API agent",
		Status:        "online",
		Capabilities:  []string{"docker", "network", "firecracker", "libvirt"},
		Labels:        map[string]string{"execution": "in-process"},
		LastHeartbeat: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
