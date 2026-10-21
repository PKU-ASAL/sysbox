package api

import (
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/persistence"
	"io"
	"net/http"
	"net/url"
	"reflect"
)

func (s *Server) handleRevisionRetentionApply(w http.ResponseWriter, r *http.Request) {
	var requested RevisionRetentionPlan
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&requested); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, fmt.Errorf("one plan object required"))
		return
	}
	if len(requested.Candidates) < 1 || len(requested.Candidates) > 128 || requested.Digest == "" {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	u, err := url.Parse(s.stateBackend)
	if err != nil || !(u.Scheme == "sqlite" || u.Scheme == "postgres" || u.Scheme == "postgresql") {
		writeError(w, 409, fmt.Errorf("batch retention requires shared SQL catalog"))
		return
	}
	if selected := u.Query().Get("topology"); selected != "" && selected != "{topology}" {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	current, err := s.planRevisionRetention(r.Context(), requested.Policy)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	if !reflect.DeepEqual(current, requested) {
		writeError(w, 409, fmt.Errorf("%w: retention preview changed", persistence.ErrConflict))
		return
	}
	repo, err := persistence.OpenCatalog(r.Context(), s.stateBackend)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer repo.Close()
	// Preview comparison is a usability check. The transaction independently
	// revalidates keep-last/age and all references for the entire batch.
	if err = repo.DeleteGlobalRevisionBatch(r.Context(), requested.Candidates, requested.Policy.OlderThan, requested.Policy.KeepLast); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": requested.Candidates, "atomic": true, "preview_digest": requested.Digest})
}
