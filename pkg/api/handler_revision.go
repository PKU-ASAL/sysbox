package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// maxRevisionBytes caps the project tree accepted by POST /v1/revisions.
const maxRevisionBytes = 8 << 20

// relPathSegment matches a single safe path segment within a project file path.
// Unlike validatePathSegment (which guards topology/node/id names and forbids
// dots), project files legitimately contain dots ("main.hcl",
// "playbook.tar.gz"), so this alphabet is wider. validateRelPath rejects "."
// and ".." segments outright before this regex is consulted.
var relPathSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateRelPath rejects any path that could escape the project root. Every
// segment must be non-empty, not "." or "..", free of backslashes, and not an
// absolute path.
func validateRelPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.ContainsRune(p, '\\') {
		return fmt.Errorf("invalid project path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid project path %q", p)
		}
		if !relPathSegment.MatchString(seg) {
			return fmt.Errorf("invalid project path %q", p)
		}
	}
	return nil
}

// handlePublishRevision stores a content-addressed project directory tree and
// returns its digest. Publishing the same tree twice yields the same revision,
// so callers can treat the endpoint as idempotent. The revision is global: it
// is not bound to any topology.
//
// The wire format is JSON: {"files": {"<relpath>": "<content>"}}. File content
// is a UTF-8 string; binary files are a future follow-up (base64/multipart are
// deliberately not implemented yet). Like a git object store, the registry
// accepts bytes as-is and defers HCL validation to the apply step (which
// resolves and evaluates the tree).
func (s *Server) handlePublishRevision(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRevisionBytes)
	var req struct {
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("files is required"))
		return
	}

	files := make(map[string][]byte, len(req.Files))
	size := 0
	for p, c := range req.Files {
		if err := validateRelPath(p); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		files[p] = []byte(c)
		size += len(c)
	}

	revision := controlplane.ComputeRevisionDigest(files)
	rev := controlplane.GlobalRevision{
		Revision:  revision,
		Files:     files,
		Size:      size,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.apiStore.SaveGlobalRevision(r.Context(), rev); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"revision": revision})
}
