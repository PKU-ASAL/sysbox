package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// maxRevisionBytes caps the HCL blob accepted by POST /v1/revisions.
const maxRevisionBytes = 8 << 20

// handlePublishRevision stores a content-addressed HCL blob and returns its
// digest. Publishing the same HCL twice yields the same revision, so callers
// can treat the endpoint as idempotent. The revision is global: it is not
// bound to any topology.
func (s *Server) handlePublishRevision(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRevisionBytes)
	hcl, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
		return
	}
	if len(hcl) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("hcl is required"))
		return
	}

	sum := sha256.Sum256(hcl)
	revision := "sha256:" + hex.EncodeToString(sum[:])
	rev := controlplane.GlobalRevision{
		Revision:  revision,
		HCL:       string(hcl),
		Size:      len(hcl),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.apiStore.SaveGlobalRevision(r.Context(), rev); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"revision": revision})
}
