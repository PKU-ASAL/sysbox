package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Revision delivery is scoped to the currently claimed execution, not a
// caller-selected digest or a filesystem path on the API host.
func (s *Server) handleRunRevision(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, 401, err)
		return
	}
	owner, err := s.apiStore.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if owner.IsBlocked() {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	var proof controlplane.Run
	if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
		writeError(w, 400, err)
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if run.AgentID != agentID || run.Status != controlplane.RunRunning || !run.LeaseUntil.After(time.Now().UTC()) || run.LeaseOwner == "" || run.LeaseOwner != proof.LeaseOwner || run.Attempt != proof.Attempt || !controlplane.SameRunIncarnation(*run, proof) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	revision, err := s.apiStore.GetGlobalRevision(r.Context(), run.Revision)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if revision.Revision != run.Revision || controlplane.ComputeRevisionDigest(revision.Files) != run.Revision {
		writeError(w, 500, persistence.ErrCorruptRecord)
		return
	}
	writeJSON(w, http.StatusOK, revision)
}
