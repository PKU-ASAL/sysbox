package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.agentService().List(r.Context())
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	scrubAgentSecretValues(agents)
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	var report controlplane.Agent
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode agent: %w", err))
		return
	}
	agent, err := s.agentService().Register(r.Context(), report)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	scrubAgentSecret(agent)
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	s.handleGetAgentByID(w, r.PathValue("agent"))
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent")
	if err := controlstore.ValidatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req persistence.AgentPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode agent update: %w", err))
		return
	}
	agent, err := s.agentService().UpdatePolicy(r.Context(), id, req)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	scrubAgentSecret(agent)
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleGetAgentByID(w http.ResponseWriter, id string) {
	if err := controlstore.ValidatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent, err := s.agentService().Get(context.Background(), id)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	scrubAgentSecret(agent)
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleClaimAgentRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	agentID := r.PathValue("agent")
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var expected controlplane.RunClaim
	if err := json.NewDecoder(r.Body).Decode(&expected); err != nil || expected.QueuedAt.IsZero() {
		writeError(w, http.StatusBadRequest, fmt.Errorf("run claim requires queued_at and revision from dispatched run"))
		return
	}
	if expected.ClaimID != "" {
		parsed, err := uuid.Parse(expected.ClaimID)
		if err != nil || parsed == uuid.Nil || parsed.String() != expected.ClaimID {
			writeError(w, http.StatusBadRequest, fmt.Errorf("claim_id must be a canonical nonzero UUID"))
			return
		}
	}
	s.handleClaimAgentRunByID(w, agentID, r.PathValue("id"), expected)
}

func (s *Server) handleClaimAgentRunByID(w http.ResponseWriter, agentID, runID string, expected controlplane.RunClaim) {
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := controlstore.ValidatePathSegment(runID, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.jobs.claimExpected(runID, agentID, &expected)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, persistence.ErrConflict) || errors.Is(err, persistence.ErrNotFound) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRenewAgentRun(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	runID := r.PathValue("id")
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := controlstore.ValidatePathSegment(runID, "id"); err != nil {
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
	ttl := s.cfg.RunRenewTTL()
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

func (s *Server) handleListAgentProjections(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inventory, err := s.apiStore.GetAgentInventory(r.Context(), agentID)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	projections := make([]controlplane.Projection, 0, len(inventory.Topologies))
	fresh := controlplane.FreshHostObservation(inventory.ObservedAt, time.Now())
	for _, item := range inventory.Topologies {
		health := item.Health
		if !fresh {
			health = "unknown"
		}
		projections = append(projections, controlplane.Projection{AgentID: agentID, Workspace: item.Workspace, Topology: item.Topology, Backend: item.Backend, Serial: item.Serial, ResourceCount: item.ResourceCount, Health: health, UpdatedAt: inventory.ObservedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projections": projections})
}

func (s *Server) handleListAgentCommandEvents(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
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
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	commands, err := s.apiStore.ListAgentCommands(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range commands {
		commands[i].ExecutionRequest = controlplane.GuestExecutionRequest{}
		if commands[i].Execution != nil {
			sanitized := publicGuestExecution(*commands[i].Execution)
			commands[i].Execution = &sanitized
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": commands})
}

func (s *Server) handleCancelAgentCommand(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	commandID := r.PathValue("command")
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := controlstore.ValidatePathSegment(commandID, "command"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cmd, err := s.agentService().FindCommand(r.Context(), agentID, commandID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if commandTerminal(cmd.Status) {
		writeJSON(w, http.StatusOK, cmd)
		return
	}
	cancelCmd, err := s.agentService().PublishCommand(r.Context(), agentID, controlplane.AgentCommand{
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

func (s *Server) handleGetAgentInventory(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
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
	if err := controlstore.ValidatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.agentStreams().ServeCommands(w, r, id)
}

func inventoryStatus(observed time.Time) string {
	if observed.IsZero() {
		return "unknown"
	}
	if !controlplane.FreshHostObservation(observed, time.Now()) {
		return "stale"
	}
	return "fresh"
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.handleAgentHeartbeatByID(w, r, r.PathValue("agent"))
}

func (s *Server) handleAgentHeartbeatByID(w http.ResponseWriter, r *http.Request, id string) {
	if err := controlstore.ValidatePathSegment(id, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyAgentRequest(r, id); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var report controlplane.Agent
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode agent heartbeat: %w", err))
			return
		}
	}
	report.ID = id
	agent, err := s.agentService().Heartbeat(r.Context(), report)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	scrubAgentSecret(agent)
	writeJSON(w, http.StatusOK, agent)
}

func agentRepositoryErrorStatus(err error) int {
	switch {
	case errors.Is(err, persistence.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, persistence.ErrInvalidRecord):
		return http.StatusBadRequest
	case errors.Is(err, persistence.ErrConflict):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
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
