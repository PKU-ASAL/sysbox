package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Derived SSH mutations share the parent's command, but own a distinct digest,
// resource epoch, and signed completion. They never create NodeOperations.
func (s *Server) handleDeriveSSHExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, http.StatusServiceUnavailable, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var request controlplane.SSHGrantDerivation
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if controlplane.VerifySSHGrantDerivation(s.grantHostKeys[r.PathValue("agent")], request) != nil {
		writeError(w, http.StatusUnauthorized, controlplane.ErrInvalidExecutionGrant)
		return
	}
	digest, err := request.Request.Digest()
	if err != nil || request.ResourceUID == "" || request.Generation < 1 || request.GuestBootID == "" {
		writeError(w, http.StatusBadRequest, persistence.ErrInvalidRecord)
		return
	}
	parent := request.Parent
	if err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).ObserveTime(r.Context()); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if parent.Grant.AgentID != r.PathValue("agent") || controlplane.VerifyExecutionGrant(s.grantSigner.Public().(ed25519.PublicKey), parent, parent.BootID, time.Now()) != nil {
		writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
		return
	}
	operation := "guest_ssh_access_apply"
	if request.Request.Delete {
		operation = "guest_ssh_access_delete"
	}
	child := controlplane.ExecutionGrant{TargetBootID: request.GuestBootID, Scope: controlplane.ExecutionScope{TopologyUID: parent.Grant.Scope.TopologyUID, ResourceUID: request.ResourceUID, Generation: request.Generation}, AgentID: parent.Grant.AgentID, SessionID: parent.Grant.SessionID, ActionID: request.Request.ID, CommandID: parent.Grant.CommandID, Attempt: parent.Grant.Attempt, Operation: operation, Digest: digest, ExpiresAt: parent.Grant.ExpiresAt}
	derived, err := mustDeriveGuestGrant(r.Context(), s.derivedGuestStorage(r.Context(), parent), parent.Grant, child, time.Until(parent.Grant.ExpiresAt))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	signed, err := controlplane.SignExecutionGrant(s.grantSigner, *derived, request.GuestBootID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, signed)
}

func (s *Server) handleDerivedSSHCompletion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Receipt controlplane.SignedExecutionStopReceipt `json:"receipt"`
		Request substrate.SSHAccessMutation             `json:"request"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	receipt := body.Receipt
	g := receipt.Grant
	unknown := receipt.SSHStop != nil
	valid := false
	if unknown {
		valid = controlplane.VerifyGuestSSHUnknownStopReceipt(s.grantHostKeys[g.AgentID], receipt, body.Request, receipt.SSHStop.Handle.NodeID) == nil
	} else if receipt.SSHResult != nil {
		valid = controlplane.VerifyGuestSSHAccessStopReceipt(s.grantHostKeys[g.AgentID], receipt, body.Request, receipt.SSHResult.Handle.NodeID) == nil
	}
	if g.AgentID != r.PathValue("agent") || g.ActionID != r.PathValue("operation") || g.ParentEpoch == 0 || !controlplane.IsGuestSSHAccessOperation(g.Operation) || !valid {
		writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
		return
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sum := sha256.Sum256(raw)
	// The ledger checks the exact issued grant including ParentEpoch. Historical
	// signed completion remains admissible after expiry; it grants no new effect.
	err = persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).AcceptHostStop(r.Context(), g, hex.EncodeToString(sum[:]))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"completed": !unknown, "stopped": true})
}
