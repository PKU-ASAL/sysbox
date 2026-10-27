package api

import (
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/persistence"
	"io"
	"net/http"
	"net/url"
	"time"
)

func (s *Server) handleRevisionDelete(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision  string    `json:"revision"`
		OlderThan time.Time `json:"older_than"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, fmt.Errorf("one JSON object required"))
		return
	}

	if _, ok := s.apiStore.(*localAPIStore); ok && s.stateBackend == "" {
		if err := s.deleteLocalRevisionBatch(r.Context(), []string{input.Revision}, RevisionRetentionPolicy{OlderThan: input.OlderThan, Limit: 1}); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]any{"revision": input.Revision, "deleted": true, "backend": "file"})
		return
	}
	u, err := url.Parse(s.stateBackend)
	if err != nil || !(u.Scheme == "sqlite" || u.Scheme == "postgres" || u.Scheme == "postgresql") {
		writeError(w, 409, fmt.Errorf("revision deletion requires shared SQL registry and lifecycle catalog"))
		return
	}
	if selected := u.Query().Get("topology"); selected != "" && selected != "{topology}" {
		writeError(w, 409, fmt.Errorf("global revision deletion requires unscoped catalog"))
		return
	}
	repo, err := persistence.OpenCatalog(r.Context(), s.stateBackend)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer repo.Close()
	if err = repo.DeleteGlobalRevision(r.Context(), input.Revision, input.OlderThan); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": input.Revision, "deleted": true})
}
