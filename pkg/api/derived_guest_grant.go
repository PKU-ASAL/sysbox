package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// derivedGuestStorage revalidates the original command while holding the same
// durable lock as the authority ledger. Neither deriving nor renewing a child
// may outlive the command that authorized its parent.
func (s *Server) derivedGuestStorage(ctx context.Context, parent controlplane.SignedExecutionGrant) apiExecutionGrantStorage {
	g := parent.Grant
	return s.commandGrantStorage(g.CommandID, func(record *persistence.CommandRecord) error {
		if record == nil {
			return persistence.ErrConflict
		}
		proof := ExecutionGrantRequest{SessionID: g.SessionID, LeaseOwner: record.Command.LeaseOwner, Attempt: int(g.Attempt), BootID: parent.BootID}
		current, err := s.sourceGrant(ctx, g.AgentID, proof, record)
		if err != nil {
			return err
		}
		if current.Scope != g.Scope || current.AgentID != g.AgentID || current.SessionID != g.SessionID || current.TargetBootID != g.TargetBootID || current.ActionID != g.ActionID || current.CommandID != g.CommandID || current.Attempt != g.Attempt || current.Operation != g.Operation || current.Digest != g.Digest || g.ExpiresAt.After(current.ExpiresAt) {
			return persistence.ErrConflict
		}
		return nil
	})
}

func (s *Server) handleRenewDerivedGuestExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, http.StatusServiceUnavailable, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var request struct {
		Parent controlplane.SignedExecutionGrant `json:"parent"`
		Child  controlplane.SignedExecutionGrant `json:"child"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).ObserveTime(r.Context()); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	key, now := s.grantSigner.Public().(ed25519.PublicKey), time.Now().UTC()
	for _, signed := range []controlplane.SignedExecutionGrant{request.Parent, request.Child} {
		if signed.Grant.AgentID != r.PathValue("agent") || controlplane.VerifyExecutionGrant(key, signed, signed.BootID, now) != nil {
			writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
			return
		}
	}
	renewed, err := persistence.NewExecutionGrantRepository(s.derivedGuestStorage(r.Context(), request.Parent)).RenewDerivedGuestTask(r.Context(), request.Parent.Grant, request.Child.Grant, time.Until(request.Parent.Grant.ExpiresAt))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	signed, err := controlplane.SignExecutionGrant(s.grantSigner, *renewed, request.Child.BootID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, signed)
}

func (s *Server) authorizeDerivedGuestGrant(w http.ResponseWriter, r *http.Request, child controlplane.ExecutionGrant) {
	if child.AgentID != r.PathValue("agent") {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	base := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	parent, err := base.InspectGrant(r.Context(), child.AgentID, controlplane.ExecutionScope{TopologyUID: child.Scope.TopologyUID})
	if err != nil || parent == nil || parent.Epoch != child.ParentEpoch {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	// This snapshot supplies immutable parent identity to the command validator.
	// Authorize rechecks the child's linked parent under the authority lock, so
	// retirement, revocation or replacement between the reads cannot authorize it.
	storage := s.derivedGuestStorage(r.Context(), controlplane.SignedExecutionGrant{Grant: *parent, BootID: parent.TargetBootID})
	if err := persistence.NewExecutionGrantRepository(storage).Authorize(r.Context(), child); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authorized": true})
}
