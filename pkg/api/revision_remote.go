package api

import (
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/revisiongate"
)

func (s *Server) handleRemoteRevisionSync(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RevisionRemote == nil || s.stateBackend != "" {
		writeError(w, 409, fmt.Errorf("remote consumer not configured"))
		return
	}
	if err := revisiongate.SyncRemote(r.Context(), s.runsDir); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"synchronized": true})
}
