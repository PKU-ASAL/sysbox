package api

import (
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleFenceIntent(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	var body struct {
		Decision             controlplane.IntentDispositionDecision `json:"decision"`
		ObservationCommandID string                                 `json:"observation_command_id"`
	}
	if err := decodeFenceBody(w, r, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	d := body.Decision
	if _, err := d.Digest(); err != nil {
		writeError(w, 400, err)
		return
	}
	if r.Header.Get("Idempotency-Key") != d.ID || controlstore.ValidatePathSegment(body.ObservationCommandID, "observation") != nil {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	fence, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if fence.Request.ID != d.FenceID || fence.Request.Topology != d.Topology {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if rec := fence.IntentDisposition; rec != nil {
		hash, _ := d.Digest()
		prior, _ := rec.Decision.Digest()
		if hash != prior || rec.Observation.Command.ID != body.ObservationCommandID {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		if !rec.ReleasedAt.IsZero() {
			writeJSON(w, 200, rec)
			return
		}
	}
	request := persistence.HostFenceReleaseRequest{FenceID: d.FenceID, Key: d.ID, ObservationCommandID: body.ObservationCommandID}
	result, err := s.reconcileFenceIntent(r.Context(), fence, request, d)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, result)
}
