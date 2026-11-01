package api

import (
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleSnapshotRetentionPlan(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	topology := r.PathValue("topology")
	if controlstore.ValidatePathSegment(topology, "topology") != nil {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	var policy persistence.SnapshotRetentionPolicy
	if err := decodeFenceBody(w, r, &policy); err != nil {
		writeError(w, 400, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	plan, err := repo.PlanSnapshotRetention(r.Context(), topology, policy)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, plan)
}
func (s *Server) handleSnapshotRetentionApply(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	topology := r.PathValue("topology")
	if controlstore.ValidatePathSegment(topology, "topology") != nil {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	var plan persistence.SnapshotRetentionPlan
	if err := decodeFenceBody(w, r, &plan); err != nil {
		writeError(w, 400, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	removed, err := repo.ApplySnapshotRetention(r.Context(), topology, plan)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": removed, "resources_deleted": false})
}
