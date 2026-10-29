package api

import (
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleFenceDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	var body struct {
		ObservationCommandID string `json:"observation_command_id"`
	}
	if err := decodeFenceBody(w, r, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	if controlstore.ValidatePathSegment(body.ObservationCommandID, "observation") != nil {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	fence, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	request := persistence.HostFenceReleaseRequest{FenceID: fence.Request.ID, Key: key, ObservationCommandID: body.ObservationCommandID}
	if rec := fence.Reconciliation; rec != nil && rec.Completed {
		if rec.Key != key || rec.ObservationCommandID != body.ObservationCommandID {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 200, rec)
		return
	}
	result, err := s.reconcileFenceDelete(r.Context(), fence, request)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, result)
}
