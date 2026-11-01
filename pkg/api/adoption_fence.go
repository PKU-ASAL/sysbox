package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleBeginAdoptionFence(w http.ResponseWriter, r *http.Request) {
	topology, id, key := r.PathValue("topology"), r.PathValue("operation"), r.Header.Get("Idempotency-Key")
	if controlstore.ValidatePathSegment(topology, "topology") != nil || controlstore.ValidatePathSegment(id, "operation") != nil || key == "" || len(key) > 128 {
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
	op, err := s.apiStore.GetNodeOperation(r.Context(), id)
	if err != nil || op.Adoption == nil || op.Topology != topology || op.Status == controlplane.NodeOperationStatusDone {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	target, ok := s.fenceTargets[op.AgentID]
	digest, err := op.Adoption.Digest()
	g := body.Grant
	if !ok || err != nil || g.Operation != "adoption_apply" || g.Digest != digest || g.CommandID != op.CommandID || g.ActionID != id || g.AgentID != op.AgentID || g.Scope != (controlplane.ExecutionScope{TopologyUID: op.Adoption.TopologyUID, ResourceUID: op.ResourceUID, Generation: op.Generation}) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	request := controlplane.HostFenceRequest{Version: 1, ID: fmt.Sprintf("adf-%x", sha256.Sum256([]byte(topology+"\x00"+key))), Topology: topology, RunID: id, LabID: target.LabID, ControllerKey: target.PublicKey, JournalSHA256: digest, Grant: g}
	bound := s.commandGrantStorage(g.CommandID, func(record *persistence.CommandRecord) error {
		if record == nil || record.Retired || !adoptionFenceCommand(record.Command, *op.Adoption, g) {
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
func adoptionFenceCommand(c controlplane.AgentCommand, a controlplane.AdoptionRequest, g controlplane.ExecutionGrant) bool {
	if c.Type != "node_operation" || c.ID != g.CommandID || c.ActionID != a.ID || c.AgentID != a.AgentID || c.Operation.Adoption == nil {
		return false
	}
	digest, err := c.Operation.Adoption.Digest()
	expected, e := a.Digest()
	return err == nil && e == nil && digest == expected && c.TopologyUID == a.TopologyUID && c.ResourceUID == a.ResourceUID && c.Generation == a.Generation
}
func (s *Server) handleAuthorizeAdoptionFence(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	f, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).ReadHostFence(r.Context(), r.PathValue("fence"))
	if err != nil || f.Request.Grant.AgentID != r.PathValue("agent") || f.Request.Grant.Operation != "adoption_apply" || f.Receipt == nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if f.AdoptionDisposition != nil {
		if !f.AdoptionDisposition.ReleasedAt.IsZero() {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 200, f.AdoptionDisposition.Proof)
		return
	}
	op, err := s.apiStore.GetNodeOperation(r.Context(), f.Request.RunID)
	if err != nil || op.Adoption == nil || op.Status == controlplane.NodeOperationStatusDone {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	digest, err := op.Adoption.Digest()
	if err != nil || digest != f.Request.JournalSHA256 {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	writeJSON(w, 200, controlplane.AdoptionDispositionProof{Request: f.Request, Fence: *f.Receipt, Adoption: *op.Adoption})
}
func (s *Server) handleAdoptionFenceDisposition(w http.ResponseWriter, r *http.Request) {
	f, err := s.readHostFence(r)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	var proof controlplane.AdoptionDispositionProof
	if err = decodeFenceBody(w, r, &proof); err != nil {
		writeError(w, 400, err)
		return
	}
	if controlstore.GcEvidenceHash(proof.Request) != controlstore.GcEvidenceHash(f.Request) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if err = s.reconcileAdoptionFence(r.Context(), proof); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"operation_result": "unknown", "disposition": proof.Disposition(), "fence": proof.Request.ID})
}
func (s *Server) reconcileAdoptionFence(ctx context.Context, p controlplane.AdoptionDispositionProof) error {
	hostKey := s.grantHostKeys[p.Request.Grant.AgentID]
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		return persistence.ErrConflict
	}
	transferKey := s.grantSigner.Public().(ed25519.PublicKey)
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	if err := authority.PrepareAdoptionDisposition(ctx, p, hostKey); err != nil {
		return err
	}
	hash := persistence.AdoptionDispositionHash(p)
	storage, err := s.fenceCommandStorage()
	if err != nil {
		return err
	}
	_, err = storage.UpdateCommand(ctx, p.Request.Grant.CommandID, func(record *persistence.CommandRecord) (*persistence.CommandRecord, error) {
		if record == nil || !adoptionFenceCommand(record.Command, p.Adoption, p.Request.Grant) {
			return nil, persistence.ErrConflict
		}
		record.Retired = true
		record.Command.Status = controlplane.AgentCommandStatusFailed
		record.Command.Err = "adoption fenced; original result unknown; disposition " + hash
		record.Command.EndedAt = p.ObservedAt
		return record, nil
	})
	if err != nil {
		return err
	}
	op, err := s.apiStore.GetNodeOperation(ctx, p.Adoption.ID)
	if err != nil {
		return err
	}
	message := "adoption fenced; original result unknown; disposition " + hash
	if op.Status != controlplane.NodeOperationStatusFailed {
		if op.Status == controlplane.NodeOperationStatusDone || op.Adoption == nil {
			return persistence.ErrConflict
		}
		digest, err := op.Adoption.Digest()
		if err != nil || digest != p.Request.JournalSHA256 {
			return persistence.ErrConflict
		}
		next := *op
		next.Status = controlplane.NodeOperationStatusFailed
		next.Err = message
		next.EndedAt = p.ObservedAt
		if err = s.apiStore.CompareAndSwapNodeOperation(ctx, *op, next); err != nil {
			return err
		}
	}
	if err = s.commitFencedAdoption(ctx, p.Adoption.Topology, p, hostKey, transferKey); err != nil {
		return err
	}
	bound := s.commandGrantStorage(p.Request.Grant.CommandID, func(record *persistence.CommandRecord) error {
		if record == nil || !record.Retired || record.Command.Err != message || !adoptionFenceCommand(record.Command, p.Adoption, p.Request.Grant) {
			return persistence.ErrConflict
		}
		return nil
	})
	bound.Lifecycle = nil
	if err = persistence.NewExecutionGrantRepository(bound).FinishAdoptionDisposition(ctx, p, hash); err != nil {
		return err
	}
	if p.Adoption.Transfer != nil {
		plan := p.Adoption.Transfer.Plan
		barrier, err := authority.InspectMaintenance(ctx, plan.ID)
		if err != nil {
			return err
		}
		digest, err := plan.Digest()
		if err != nil || barrier.TopologyUID != plan.Source.TopologyUID || barrier.RequestDigest != digest {
			return persistence.ErrConflict
		}
		if !barrier.Released {
			return authority.EndMaintenance(ctx, *barrier)
		}
	}
	return nil
}
