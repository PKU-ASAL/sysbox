package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handlePlanSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	topology, snapshot, id := r.PathValue("topology"), r.PathValue("snapshot"), r.Header.Get("Idempotency-Key")
	for _, value := range []string{topology, snapshot, id} {
		if err := validatePathSegment(value, "restore identity"); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	plan, err := repo.PlanSnapshotRestore(r.Context(), topology, snapshot, id, 0)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleCancelSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	topology, snapshot := r.PathValue("topology"), r.PathValue("snapshot")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	var request persistence.SnapshotRestoreRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.SnapshotID != snapshot {
		writeError(w, 400, fmt.Errorf("matching restore plan required"))
		return
	}
	digest, err := request.Digest()
	if err != nil {
		writeError(w, 400, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	_, err = repo.ReadSnapshotRestoreReceipt(r.Context(), topology, request.ID)
	if err == nil {
		writeError(w, 409, fmt.Errorf("restore already committed; retry original restore to finish release"))
		return
	}
	if !errors.Is(err, persistence.ErrNotFound) {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	current, err := repo.LoadState(r.Context(), topology)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	if current.TopologyUID != request.TopologyUID {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	barrier, err := authority.InspectMaintenance(r.Context(), request.ID)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	if barrier.TopologyUID != request.TopologyUID || barrier.RequestDigest != digest || request.MaintenanceEpoch != 0 && request.MaintenanceEpoch != barrier.Epoch {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if err := authority.EndMaintenance(r.Context(), *barrier); err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	// A commit may have won the authority lock after the initial read. Once
	// release completes, no new commit can start; report the actual winner.
	if receipt, err := repo.ReadSnapshotRestoreReceipt(r.Context(), topology, request.ID); err == nil {
		writeJSON(w, 200, snapshotRestoreResult{Status: "restored", Request: receipt.Request, Receipt: &receipt})
		return
	} else if !errors.Is(err, persistence.ErrNotFound) {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "abandoned", "request_id": request.ID})
}
