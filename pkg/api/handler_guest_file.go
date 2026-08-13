package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
)

const guestFileMaxSize = 16 << 20

type guestFilePayload struct {
	AgentID   string    `json:"agent_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) handlePutGuestFile(w http.ResponseWriter, r *http.Request) {
	s.gcGuestFilePayloads(time.Now().UTC())
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
	meta := guestFilePayload{AgentID: agentID, ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}
	if err := writeLocalObject(filepath.Join(dir, id+".json"), meta); err != nil {
		_ = os.Remove(filepath.Join(dir, id))
		writeError(w, 500, err)
		return
	}
	if _, err = s.agentService().PublishCommand(r.Context(), agentID, controlplane.AgentCommand{Type: "guest_file_put", FilePut: &put}); err != nil {
		_ = os.Remove(filepath.Join(dir, id))
		writeError(w, 500, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "queued"})
}

func (s *Server) gcGuestFilePayloads(now time.Time) {
	dir := filepath.Join(s.runsDir, "_guest-files")
	entries, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, entry := range entries {
		var meta guestFilePayload
		raw, err := os.ReadFile(entry)
		if err != nil || json.Unmarshal(raw, &meta) != nil || !now.Before(meta.ExpiresAt) {
			base := strings.TrimSuffix(entry, ".json")
			_ = os.Remove(base)
			_ = os.Remove(entry)
		}
	}
}

func (s *Server) handleFetchGuestFile(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyAgentRequest(r, r.PathValue("agent")); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	path := filepath.Join(s.runsDir, "_guest-files", filepath.Base(r.PathValue("file")))
	var meta guestFilePayload
	raw, err := os.ReadFile(path + ".json")
	if err != nil || json.Unmarshal(raw, &meta) != nil || meta.AgentID != r.PathValue("agent") || time.Now().UTC().After(meta.ExpiresAt) {
		_ = os.Remove(path)
		_ = os.Remove(path + ".json")
		writeError(w, http.StatusNotFound, fmt.Errorf("file payload not found"))
		return
	}
	consuming := path + ".consuming"
	if err := os.Rename(path, consuming); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file payload not found"))
		return
	}
	_ = os.Remove(path + ".json")
	f, err := os.Open(consuming)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file payload not found"))
		return
	}
	defer f.Close()
	defer os.Remove(consuming)
	if _, err := io.Copy(w, f); err == nil {
		_ = os.Remove(consuming)
	}
}
