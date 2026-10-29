package api

import (
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleHostFenceRelease(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	var body struct {
		ObservationCommandID string `json:"observation_command_id"`
	}
	if key == "" || len(key) > 128 {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
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
	result, err := s.releaseHostFence(r.Context(), fence, persistence.HostFenceReleaseRequest{FenceID: fence.Request.ID, Key: key, ObservationCommandID: body.ObservationCommandID})
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, result)
}
