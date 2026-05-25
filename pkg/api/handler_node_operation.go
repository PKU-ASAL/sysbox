package api

import (
	"net/http"
)

func (s *Server) handleGetNodeOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("operation")
	if err := validatePathSegment(id, "operation"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	op, err := s.nodeOps.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
