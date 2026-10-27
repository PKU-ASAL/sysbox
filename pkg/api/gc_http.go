package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		writeError(w, 400, fmt.Errorf("Idempotency-Key required (1-128 bytes)"))
		return
	}
	var candidate controlplane.GCCandidate
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidate); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := candidate.Validate(); err != nil {
		writeError(w, 400, err)
		return
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	placement, err := s.workspaceService().loadPlacement(topology)
	if err != nil || placement == nil {
		writeError(w, 409, fmt.Errorf("GC requires durable owning-agent placement: %v", err))
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize || len(s.grantHostKeys[placement.AgentID]) != ed25519.PublicKeySize {
		writeError(w, 503, fmt.Errorf("GC requires issuer and enrolled host keys"))
		return
	}
	id := fmt.Sprintf("gc-%x", sha256.Sum256([]byte(topology+"\x00"+key)))
	reservation := persistence.GCReservation{ID: id, AgentID: placement.AgentID, Candidate: candidate}
	if err := s.reserveGCWithAuthority(r.Context(), topology, reservation); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	reservation, err = repo.LoadGCReservation(r.Context(), topology, id)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	if err := s.publishGCReservation(r.Context(), topology, reservation); err != nil {
		writeError(w, 503, err)
		return
	}
	writeJSON(w, http.StatusAccepted, reservation)
}

// The reservation is the outbox seed. Publication may be retried after process
// failure; an existing or retired command is never reset to queued.
func (s *Server) publishGCReservation(ctx context.Context, topology string, reservation persistence.GCReservation) error {
	if !reservation.Pending() {
		return nil
	}
	prior, err := readFailureCommand(ctx, s.apiStore, reservation.ID)
	if err == nil && prior != nil {
		if prior.Command.AgentID != reservation.AgentID {
			return persistence.ErrConflict
		}
		return nil
	}
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return err
	}
	c := reservation.Candidate
	cmd := controlplane.AgentCommand{ID: reservation.ID, AgentID: reservation.AgentID, Type: "gc_delete", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, CreatedAt: time.Now().UTC(), TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation, ActionID: reservation.ID, GCKind: c.Kind, GCExternalID: c.ExternalID, GCInventoryDigest: c.InventoryDigest, Session: &controlplane.ConsoleSession{Topology: topology, AgentID: reservation.AgentID}}
	if err := s.apiStore.SaveAgentCommand(ctx, cmd); err != nil {
		return err
	}
	if s.agents != nil {
		_ = s.agents.PublishCommand(cmd.AgentID, cmd)
	}
	return nil
}

func (s *Server) reconcileGCOutbox(ctx context.Context, topology string) error {
	repo, err := s.actionRepository(topology)
	if err != nil {
		return err
	}
	reservations, err := repo.ListGCReservations(ctx, topology)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if !reservation.Pending() {
			continue
		}
		if err := s.publishGCReservation(ctx, topology, reservation); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func (s *Server) handleGCReferences(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	refs, err := repo.LoadGCReferences(r.Context(), topology)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, refs)
}
