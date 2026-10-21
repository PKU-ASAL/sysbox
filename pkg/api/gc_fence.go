package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func gcEvidenceHash(v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (s *Server) handleBeginGCFence(w http.ResponseWriter, r *http.Request) {
	topology, id, key := r.PathValue("topology"), r.PathValue("reservation"), r.Header.Get("Idempotency-Key")
	if validatePathSegment(topology, "topology") != nil || validatePathSegment(id, "reservation") != nil || key == "" || len(key) > 128 {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	var body struct {
		Grant controlplane.ExecutionGrant `json:"grant"`
	}
	if err := decodeFenceBody(w, r, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	stored, err := repo.LoadGCReservation(r.Context(), topology, id)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	target, ok := s.fenceTargets[stored.AgentID]
	if !ok || !stored.Pending() {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	digest, err := stored.Candidate.Digest()
	if err != nil {
		writeError(w, 409, err)
		return
	}
	g := body.Grant
	if g.Operation != "gc_delete" || g.CommandID != id || g.ActionID != id || g.AgentID != stored.AgentID || g.Digest != digest || g.Scope != (controlplane.ExecutionScope{TopologyUID: stored.Candidate.TopologyUID, ResourceUID: stored.Candidate.ResourceUID, Generation: stored.Candidate.Generation}) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	request := controlplane.HostFenceRequest{Version: 1, ID: fmt.Sprintf("gcf-%x", sha256.Sum256([]byte(topology+"\x00"+key))), Topology: topology, RunID: id, LabID: target.LabID, ControllerKey: target.PublicKey, JournalSHA256: gcEvidenceHash(stored), Grant: g}
	bound := s.commandGrantStorage(id, func(record *persistence.CommandRecord) error {
		if record == nil || record.Retired || !gcCommandMatches(record.Command, stored) {
			return persistence.ErrConflict
		}
		return nil
	})
	accepted, err := persistence.NewExecutionGrantRepository(bound).BeginHostFence(r.Context(), request)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 202, accepted)
}
func gcCommandMatches(cmd controlplane.AgentCommand, r persistence.GCReservation) bool {
	c := r.Candidate
	return cmd.ID == r.ID && cmd.Type == "gc_delete" && cmd.AgentID == r.AgentID && cmd.ActionID == r.ID && cmd.TopologyUID == c.TopologyUID && cmd.ResourceUID == c.ResourceUID && cmd.Generation == c.Generation && cmd.GCKind == c.Kind && cmd.GCExternalID == c.ExternalID && cmd.GCInventoryDigest == c.InventoryDigest
}
func (s *Server) handleAuthorizeGCFence(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	id := r.PathValue("fence")
	if validatePathSegment(id, "fence") != nil {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	f, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).ReadHostFence(r.Context(), id)
	if err != nil || f.Request.Grant.AgentID != r.PathValue("agent") || f.Request.Grant.Operation != "gc_delete" || f.Receipt == nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if f.GCDisposition != nil && !f.GCDisposition.ReleasedAt.IsZero() {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if f.GCDisposition != nil {
		writeJSON(w, 200, f.GCDisposition.Proof)
		return
	}
	repo, err := s.actionRepository(f.Request.Topology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	stored, err := repo.LoadGCReservation(r.Context(), f.Request.Topology, f.Request.Grant.CommandID)
	if err != nil || !stored.Pending() || gcEvidenceHash(stored) != f.Request.JournalSHA256 {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	writeJSON(w, 200, controlplane.GCDispositionProof{Request: f.Request, Fence: *f.Receipt, Candidate: stored.Candidate})
}
func (s *Server) handleGCFenceDisposition(w http.ResponseWriter, r *http.Request) {
	f, err := s.readHostFence(r)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	var proof controlplane.GCDispositionProof
	if err = decodeFenceBody(w, r, &proof); err != nil {
		writeError(w, 400, err)
		return
	}
	if gcEvidenceHash(proof.Request) != gcEvidenceHash(f.Request) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if err = s.reconcileGCFence(r.Context(), proof); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"reservation": proof.Request.Grant.CommandID, "execution": "fenced", "operation_result": "unknown", "observed_presence": proof.Presence})
}
func (s *Server) reconcileGCFence(ctx context.Context, p controlplane.GCDispositionProof) error {
	key := s.grantHostKeys[p.Request.Grant.AgentID]
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err := authority.PrepareGCDisposition(ctx, p, key); err != nil {
		return fmt.Errorf("prepare GC disposition: %w", err)
	}
	repo, err := s.actionRepository(p.Request.Topology)
	if err != nil {
		return err
	}
	stored, err := repo.LoadGCReservation(ctx, p.Request.Topology, p.Request.Grant.CommandID)
	if err != nil {
		return err
	}
	storage, err := s.fenceCommandStorage()
	if err != nil {
		return err
	}
	_, err = storage.UpdateCommand(ctx, stored.ID, func(current *persistence.CommandRecord) (*persistence.CommandRecord, error) {
		if current == nil || !gcCommandMatches(current.Command, stored) {
			return nil, persistence.ErrConflict
		}
		current.Retired = true
		current.Command.Status = controlplane.AgentCommandStatusFailed
		current.Command.Err = "host fenced; original GC outcome unknown"
		current.Command.EndedAt = p.ObservedAt
		return current, nil
	})
	if err != nil {
		return err
	}
	if err = repo.CompleteGCDisposition(ctx, p.Request.Topology, p, key); err != nil {
		return fmt.Errorf("commit GC disposition: %w", err)
	}
	stored, err = repo.LoadGCReservation(ctx, p.Request.Topology, stored.ID)
	if err != nil {
		return err
	}
	bound := s.commandGrantStorage(stored.ID, func(current *persistence.CommandRecord) error {
		if current == nil || !current.Retired || !gcCommandMatches(current.Command, stored) || current.Command.Status != controlplane.AgentCommandStatusFailed {
			return persistence.ErrConflict
		}
		return nil
	})
	// This is retirement, not new work admission: the reservation is already
	// terminal, so the ordinary pending-GC lifecycle admission check must not
	// run. Keep command+authority locking and explicit retired identity checks.
	bound.lifecycle = nil
	if err = persistence.NewExecutionGrantRepository(bound).FinishGCDisposition(ctx, p, stored.DispositionHash); err != nil {
		return fmt.Errorf("release GC disposition: %w", err)
	}
	return s.reconcileGCOutbox(ctx, p.Request.Topology)
}
