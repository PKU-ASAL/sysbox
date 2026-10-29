package api

import (
	"encoding/json"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) completeHostInspection(w http.ResponseWriter, r *http.Request, body HostNodeOperationCompletion) {
	agentID, id := r.PathValue("agent"), r.PathValue("operation")
	if body.Receipt.Grant.AgentID != agentID || body.Receipt.Grant.ActionID != id || controlplane.VerifyAdoptionStopReceipt(s.grantHostKeys[agentID], body.Receipt, body.Evidence) != nil {
		writeError(w, 409, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var result controlplane.AdoptionInspectionResult
	if err := json.Unmarshal(body.Evidence, &result); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := controlstore.ValidateHostOperationReceipt(r.Context(), s.apiStore, body.Receipt); err != nil {
		writeError(w, 409, err)
		return
	}
	transition := func(current controlplane.NodeOperation, record *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		return persistence.CompleteAdoptionInspectionFromHost(current, record, body.Receipt, result)
	}
	var completed *controlplane.NodeOperation
	var err error
	switch store := s.apiStore.(type) {
	case *controlstore.Local:
		completed, err = store.TransitionNodeOperation(r.Context(), agentID, id, &body.Receipt, transition)
	case *controlstore.SQLite:
		completed, err = store.TransitionNodeOperation(r.Context(), agentID, id, &body.Receipt, transition)
	case *controlstore.Postgres:
		completed, err = store.TransitionNodeOperation(r.Context(), agentID, id, &body.Receipt, transition)
	default:
		err = persistence.ErrUnsupportedVersion
	}
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, completed)
}
