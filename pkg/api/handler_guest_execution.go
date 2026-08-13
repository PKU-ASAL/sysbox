package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Server) authorizeGuestOperation(subj requestSubject) error {
	for _, role := range subj.Roles {
		if slices.Contains(s.cfg.API.RBAC.AdminRoles, role) {
			return nil
		}
	}
	return fmt.Errorf("guest operation denied")
}

func (s *Server) guestExecutionOwner(topology string) (string, error) {
	var owners []string
	for _, agent := range s.agentService().List(nil) {
		if !agent.IsSchedulable() {
			continue
		}
		inv, err := s.apiStore.GetAgentInventory(nil, agent.ID)
		if err != nil {
			continue
		}
		if inv.Stale {
			continue
		}
		for _, item := range inv.Topologies {
			if item.Topology == topology && item.Available {
				owners = append(owners, agent.ID)
			}
		}
	}
	if len(owners) == 0 {
		return "", fmt.Errorf("topology not found")
	}
	if len(owners) > 1 {
		return "", fmt.Errorf("topology ownership conflict")
	}
	return owners[0], nil
}

func (s *Server) guestExecutionNodeExists(topology, node string) bool {
	st, err := s.workspaceService().LoadState(topology)
	if err != nil {
		return false
	}
	return st.FindResource(address.Resource("sysbox_node", node)) != nil || st.FindResource(address.Resource("sysbox_router", node)) != nil
}

func (s *Server) handleCreateGuestExecution(w http.ResponseWriter, r *http.Request) {
	topology, node := r.PathValue("topology"), r.PathValue("node")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(node, "node"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var req controlplane.GuestExecutionRequest
	limitBody(w, r)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode guest execution request"))
		return
	}
	agentID, err := s.guestExecutionOwner(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !s.guestExecutionNodeExists(topology, node) {
		writeError(w, http.StatusNotFound, fmt.Errorf("node not found"))
		return
	}
	execution, err := s.guestExecutions().Create(r.Context(), topology, node, agentID, req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInvalidGuestExecution) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, publicGuestExecution(execution))
}

func publicGuestExecution(execution controlplane.GuestExecution) controlplane.GuestExecution {
	execution.AgentID = ""
	execution.Request = controlplane.GuestExecutionRequest{}
	return execution
}

func (s *Server) handleGetGuestExecution(w http.ResponseWriter, r *http.Request) {
	execution, err := s.guestExecutions().Get(r.Context(), r.PathValue("execution"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, http.StatusOK, publicGuestExecution(execution))
}
func (s *Server) handleCancelGuestExecution(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	execution, err := s.guestExecutions().Cancel(r.Context(), r.PathValue("execution"))
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, errGuestExecutionConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, publicGuestExecution(execution))
}
