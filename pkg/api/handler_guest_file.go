package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
)

const guestFileMaxSize = 16 << 20

func (s *Server) handlePutGuestFile(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	topology, node, dst := r.PathValue("topology"), r.PathValue("node"), r.URL.Query().Get("path")
	if !filepath.IsAbs(dst) || strings.Contains(dst, "\x00") || filepath.Clean(dst) != dst {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid guest path"))
		return
	}
	mode64, err := strconv.ParseUint(r.URL.Query().Get("mode"), 8, 12)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid file mode"))
		return
	}
	agentID, err := s.guestExecutionOwner(topology)
	if err != nil || !s.guestExecutionNodeExists(topology, node) {
		writeError(w, http.StatusNotFound, fmt.Errorf("target not found"))
		return
	}
	id := uuid.NewString()
	dir := filepath.Join(s.runsDir, "_guest-files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeError(w, 500, err)
		return
	}
	tmp, err := os.CreateTemp(dir, id+"-")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	_ = tmp.Chmod(0600)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), http.MaxBytesReader(w, r.Body, guestFileMaxSize))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp.Name())
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid file payload"))
		return
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if want := r.Header.Get("X-Content-SHA256"); want != "" && !strings.EqualFold(want, digest) {
		_ = os.Remove(tmp.Name())
		writeError(w, http.StatusBadRequest, fmt.Errorf("content digest mismatch"))
		return
	}
	put := controlplane.GuestFilePut{ID: id, Topology: topology, Node: node, Path: dst, Mode: uint32(mode64), Size: n, SHA256: digest, FetchRef: "/v1/agents/" + agentID + "/guest-files/" + id}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, id)); err != nil {
		_ = os.Remove(tmp.Name())
		writeError(w, 500, err)
		return
	}
	if _, err = s.agentService().PublishCommand(r.Context(), agentID, controlplane.AgentCommand{Type: "guest_file_put", FilePut: &put}); err != nil {
		_ = os.Remove(filepath.Join(dir, id))
		writeError(w, 500, err)
		return
	}
	writeJSON(w, http.StatusAccepted, put)
}

func (s *Server) handleFetchGuestFile(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyAgentRequest(r, r.PathValue("agent")); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	path := filepath.Join(s.runsDir, "_guest-files", filepath.Base(r.PathValue("file")))
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file payload not found"))
		return
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err == nil {
		_ = os.Remove(path)
	}
}
