package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) completeHostSSHAccess(w http.ResponseWriter, r *http.Request, body HostNodeOperationCompletion) {
	agent, id := r.PathValue("agent"), r.PathValue("operation")
	if body.Receipt.Grant.AgentID != agent || controlplane.VerifySSHAccessStopReceipt(s.grantHostKeys[agent], body.Receipt, body.Evidence) != nil {
		writeError(w, 409, controlplane.ErrInvalidExecutionGrant)
		return
	}
	op, err := s.apiStore.GetNodeOperation(r.Context(), id)
	if err != nil || op.SSHAccess == nil || op.ID != body.Receipt.Grant.ActionID || op.AgentID != agent {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	var evidence struct {
		AccessID string `json:"access_id"`
		SHA256   string `json:"sha256"`
		Deleted  bool   `json:"deleted"`
	}
	if json.Unmarshal(body.Evidence, &evidence) != nil || evidence.AccessID != op.SSHAccess.AccessID {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if op.Operation == "ssh_access_apply" {
		content, err := op.SSHAccess.AuthorizedKeysContent()
		sum := sha256.Sum256(content)
		if err != nil || evidence.Deleted || evidence.SHA256 != hex.EncodeToString(sum[:]) {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
	} else if op.Operation != "ssh_access_delete" || !evidence.Deleted {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	raw, err := json.Marshal(body.Receipt)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	sum := sha256.Sum256(raw)
	receiptID := hex.EncodeToString(sum[:])
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err := authority.ValidateHostStop(r.Context(), body.Receipt.Grant, receiptID); err != nil {
		writeError(w, 409, err)
		return
	}
	completed, err := s.apiStore.CompleteSSHAccessFromHost(r.Context(), agent, id, body.Receipt)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if err := authority.AcceptHostStop(r.Context(), body.Receipt.Grant, receiptID); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, completed)
}
