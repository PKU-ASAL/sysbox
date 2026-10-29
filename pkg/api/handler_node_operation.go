package api

import (
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
)

func (s *Server) handleGetNodeOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("operation")
	if err := controlstore.ValidatePathSegment(id, "operation"); err != nil {
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
