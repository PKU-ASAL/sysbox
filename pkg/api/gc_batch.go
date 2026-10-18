package api

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"net/http"
)

type gcBatchRequest struct {
	Candidates []controlplane.GCCandidate `json:"candidates"`
}
type gcBatchResponse struct {
	Reservations []persistence.GCReservation `json:"reservations"`
}

func (s *Server) handleGCBatch(w http.ResponseWriter, r *http.Request) {
	topology, key := r.PathValue("topology"), r.Header.Get("Idempotency-Key")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	if key == "" || len(key) > 128 {
		writeError(w, 400, fmt.Errorf("Idempotency-Key required"))
		return
	}
	var input gcBatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.Candidates) == 0 || len(input.Candidates) > 128 {
		writeError(w, 400, fmt.Errorf("candidates must contain 1..128 items"))
		return
	}
	for _, candidate := range input.Candidates {
		if err := candidate.Validate(); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	placement, err := s.workspaceService().loadPlacement(topology)
	if err != nil || placement == nil {
		writeError(w, 409, fmt.Errorf("GC requires owning placement: %v", err))
		return
	}

	if len(s.grantSigner) != ed25519.PrivateKeySize || len(s.grantHostKeys[placement.AgentID]) != ed25519.PublicKeySize {
		writeError(w, 503, fmt.Errorf("GC requires issuer and enrolled host keys"))
		return
	}
	requests, err := persistence.NewGCBatch(topology, key, placement.AgentID, input.Candidates)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	if err := s.reserveGCBatchWithAuthority(r.Context(), topology, requests); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if err := s.reconcileGCOutbox(r.Context(), topology); err != nil {
		writeError(w, 503, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	response := gcBatchResponse{}
	for _, rsv := range requests {
		stored, err := repo.LoadGCReservation(r.Context(), topology, rsv.ID)
		if err != nil {
			writeError(w, 503, err)
			return
		}
		response.Reservations = append(response.Reservations, stored)
	}
	writeJSON(w, 202, response)
}
