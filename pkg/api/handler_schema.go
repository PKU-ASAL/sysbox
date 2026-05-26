package api

import "net/http"

func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	version, err := s.apiStore.SchemaVersion(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "api",
		"version": version,
	})
}
