package api

import (
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
)

// Cancellation requests target the original command. They never manufacture a
// terminal Run or a host stop receipt; callers must query the resulting state.
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := controlstore.ValidatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), id)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if run.Status.IsTerminal() {
		writeJSON(w, http.StatusOK, run)
		return
	}
	if run.AgentID == "" {
		writeError(w, http.StatusConflict, fmt.Errorf("Run has no assigned worker"))
		return
	}
	r.SetPathValue("agent", run.AgentID)
	r.SetPathValue("command", controlstore.RunCommandID(*run))
	s.handleCancelAgentCommand(w, r)
}
