package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Preparing a transfer first reserves its target, then freezes and retires the
// source. Interrupted retries retain both holds and the same immutable plan.
func (s *Server) prepareOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) (controlplane.OwnershipRetirementProof, error) {
	var zero controlplane.OwnershipRetirementProof
	if err := p.Validate(); err != nil {
		return zero, err
	}
	if (p.Provider != "libvirt" && p.Provider != "docker" && p.Provider != "firecracker") || len(s.grantSigner) != ed25519.PrivateKeySize {
		return zero, persistence.ErrUnsupportedVersion
	}
	source, err := s.actionRepository(p.SourceTopology)
	if err != nil {
		return zero, err
	}
	if err = source.InspectOwnershipSource(ctx, p); err != nil {
		return zero, err
	}
	target, err := s.actionRepository(p.TargetTopology)
	if err != nil {
		return zero, err
	}
	if err = target.ReserveOwnershipTarget(ctx, p); err != nil {
		return zero, err
	}
	if _, err = s.beginOwnershipMaintenance(ctx, p); err != nil {
		return zero, err
	}
	if err = s.workspaceService().EnsureTopologyQuiescent(ctx, p.TargetTopology); err != nil {
		return zero, err
	}
	if err = s.workspaceService().EnsureTopologyQuiescent(ctx, p.SourceTopology); err != nil {
		return zero, err
	}
	proof, err := s.commitOwnershipRetirement(ctx, p.SourceTopology, p)
	if err != nil {
		return zero, err
	}
	return controlplane.SignOwnershipRetirement(s.grantSigner, proof)
}
func (s *Server) handleOwnershipTransfer(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	var p controlplane.OwnershipTransferPlan
	if err := decodeFenceBody(w, r, &p); err != nil {
		writeError(w, 400, err)
		return
	}
	if p.TargetTopology != r.PathValue("topology") {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	prior, readErr := s.apiStore.GetNodeOperation(r.Context(), p.ID)
	if readErr != nil && !errors.Is(readErr, persistence.ErrNotFound) {
		writeError(w, 409, readErr)
		return
	}
	if prior != nil {
		if prior.Adoption == nil || prior.Adoption.Transfer == nil || controlstore.GcEvidenceHash(prior.Adoption.Transfer.Plan) != controlstore.GcEvidenceHash(p) {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 202, prior)
		return
	}
	for _, topology := range []string{p.SourceTopology, p.TargetTopology} {
		placement, err := s.workspaceService().LoadPlacement(topology)
		if err != nil || placement == nil || placement.AgentID != p.AgentID {
			writeError(w, 409, fmt.Errorf("transfer requires same enrolled owning host"))
			return
		}
	}
	target, err := s.actionRepository(p.TargetTopology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	reserved, err := target.ReadOwnershipTarget(r.Context(), p.TargetTopology, p.ID)
	if err != nil || reserved.Aborted || reserved.DispositionHash != "" || reserved.Preparation == nil || controlstore.GcEvidenceHash(reserved.Plan) != controlstore.GcEvidenceHash(p) {
		writeError(w, 409, fmt.Errorf("host ownership preparation required before retirement"))
		return
	}
	proof, err := s.prepareOwnershipTransfer(r.Context(), p)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	req := controlplane.AdoptionRequest{Transfer: &proof, ID: p.ID, Topology: p.TargetTopology, TopologyUID: p.Target.TopologyUID, Address: p.TargetAddress, Provider: p.Provider, AgentID: p.AgentID, ExternalID: p.ExternalID, ResourceUID: p.Target.ResourceUID, Generation: p.Target.Generation, ExpectedSerial: p.TargetSerial, Evidence: substrate.AdoptionEvidence{Provider: p.Provider, ExternalID: p.ExternalID, Fingerprint: p.Fingerprint, ManagedIdentity: &p.Source}}
	op, err := s.nodeOperations().AdoptionConfirm(r.Context(), req, s.requestSubject(r))
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 202, op)
}

func (s *Server) handleBeginOwnershipPreparation(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	var p controlplane.OwnershipTransferPlan
	if err := decodeFenceBody(w, r, &p); err != nil {
		writeError(w, 400, err)
		return
	}
	if p.Validate() != nil || p.TargetTopology != r.PathValue("topology") || (p.Provider != "libvirt" && p.Provider != "docker" && p.Provider != "firecracker") {
		writeError(w, 409, persistence.ErrInvalidRecord)
		return
	}
	for _, name := range []string{p.SourceTopology, p.TargetTopology} {
		placement, err := s.workspaceService().LoadPlacement(name)
		if err != nil || placement == nil || placement.AgentID != p.AgentID {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
	}
	source, err := s.actionRepository(p.SourceTopology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	if err = source.InspectOwnershipSource(r.Context(), p); err != nil {
		writeError(w, 409, err)
		return
	}
	target, err := s.actionRepository(p.TargetTopology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	if err = target.ReserveOwnershipTarget(r.Context(), p); err != nil {
		writeError(w, 409, err)
		return
	}
	barrier, err := s.beginOwnershipMaintenance(r.Context(), p)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	if err = s.workspaceService().EnsureTopologyQuiescent(r.Context(), p.SourceTopology); err != nil {
		writeJSON(w, 202, map[string]any{"status": "waiting_for_stop", "plan": p, "barrier": barrier})
		return
	}
	writeJSON(w, 202, map[string]any{"status": "awaiting_host_preparation", "plan": p, "barrier": barrier})
}
func (s *Server) handleAuthorizeOwnershipPreparation(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	var p controlplane.OwnershipTransferPlan
	if err := decodeFenceBody(w, r, &p); err != nil {
		writeError(w, 400, err)
		return
	}
	if p.AgentID != r.PathValue("agent") || p.Validate() != nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	target, err := s.actionRepository(p.TargetTopology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	v, err := target.ReadOwnershipTarget(r.Context(), p.TargetTopology, p.ID)
	if err != nil || v.Aborted || v.DispositionHash != "" || v.CompletedHash != "" || controlstore.GcEvidenceHash(v.Plan) != controlstore.GcEvidenceHash(p) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	barrier, err := authority.InspectMaintenance(r.Context(), p.ID)
	digest, _ := p.Digest()
	if err != nil || barrier.Released || barrier.RequestDigest != digest || barrier.TopologyUID != p.Source.TopologyUID {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if err = s.workspaceService().EnsureTopologyQuiescent(r.Context(), p.SourceTopology); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, controlplane.OwnershipPreparation{Plan: p, SourceEpoch: barrier.Epoch})
}
func (s *Server) handleRecordOwnershipPreparation(w http.ResponseWriter, r *http.Request) {
	var proof controlplane.OwnershipPreparation
	if err := decodeFenceBody(w, r, &proof); err != nil {
		writeError(w, 400, err)
		return
	}
	if proof.Plan.TargetTopology != r.PathValue("topology") {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	barrier, err := authority.InspectMaintenance(r.Context(), proof.Plan.ID)
	digest, _ := proof.Plan.Digest()
	if err != nil || barrier.Released || barrier.Epoch != proof.SourceEpoch || barrier.RequestDigest != digest || proof.ObservedAt.Before(barrier.CreatedAt) || proof.ObservedAt.After(time.Now().UTC()) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	target, err := s.actionRepository(proof.Plan.TargetTopology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	if err = target.RecordOwnershipPreparation(r.Context(), proof, s.grantHostKeys[proof.Plan.AgentID]); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"prepared": true})
}

func (s *Server) handleAbortOwnershipTransfer(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	var plan controlplane.OwnershipTransferPlan
	if err := decodeFenceBody(w, r, &plan); err != nil {
		writeError(w, 400, err)
		return
	}
	if plan.TargetTopology != r.PathValue("topology") {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if err := s.abortOwnershipTransfer(r.Context(), plan); err != nil {
		writeError(w, 409, err)
		return
	}
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	barrier, err := authority.InspectMaintenance(r.Context(), plan.ID)
	if err == nil {
		digest, _ := plan.Digest()
		if barrier.RequestDigest != digest || barrier.TopologyUID != plan.Source.TopologyUID {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		if err = authority.EndMaintenance(r.Context(), *barrier); err != nil {
			writeJSON(w, 202, map[string]string{"status": "aborted_waiting_for_source_stop"})
			return
		}
	} else if !errors.Is(err, persistence.ErrNotFound) {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "aborted_before_source_retirement"})
}
