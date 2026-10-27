package api

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/state"
	"net/http"
)

func (s *Server) handleCreateStateSnapshot(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		writeError(w, 400, err)
		return
	}
	manager, err := s.stateManager(topology)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	snapshot, err := manager.Snapshot(r.Context(), request.Reason)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	writeJSON(w, 201, snapshot)
}
func (s *Server) handleDeleteStateSnapshot(w http.ResponseWriter, r *http.Request) {
	topology, id := r.PathValue("topology"), r.PathValue("snapshot")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := validatePathSegment(id, "snapshot"); err != nil {
		writeError(w, 400, err)
		return
	}
	manager, err := s.stateManager(topology)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	remover, ok := manager.Backend().(state.SnapshotDeleter)
	if !ok {
		writeError(w, 501, state.ErrUnsupported)
		return
	}
	if err := remover.DeleteSnapshot(r.Context(), id); err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
