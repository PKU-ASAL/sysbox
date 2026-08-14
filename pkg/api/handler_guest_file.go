package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	agentID, err := s.guestExecutionOwner(r.Context(), topology)
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
	cleanup := func() {
		_ = os.Remove(filepath.Join(dir, id))
		_ = os.Remove(filepath.Join(dir, id+".json"))
	}
	commandID := uuid.NewString()
	op := controlplane.GuestFileOperation{ID: id, Version: 1, AgentID: agentID, CommandID: commandID, Topology: topology, Node: node, Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
	if err := s.apiStore.SaveGuestFileOperation(r.Context(), op); err != nil {
		cleanup()
		writeError(w, 500, err)
		return
	}
	_, err = s.agentService().PublishCommand(r.Context(), agentID, controlplane.AgentCommand{ID: commandID, Type: "guest_file_put", FilePut: &put})
	if err != nil {
		op.Status, op.Error, op.EndedAt = controlplane.GuestExecutionFailed, "dispatch failed", time.Now().UTC()
		if ok, saveErr := s.apiStore.CompareAndSwapGuestFileOperation(r.Context(), op, op.Version); saveErr != nil || !ok {
			cleanup()
			writeError(w, 500, fmt.Errorf("record file operation dispatch failure"))
			return
		}
		cleanup()
		writeError(w, 500, err)
		return
	}
	writeJSON(w, http.StatusAccepted, publicGuestFileOperation(op))
}

func publicGuestFileOperation(op controlplane.GuestFileOperation) controlplane.GuestFileOperation {
	op.AgentID, op.CommandID = "", ""
	return op
}

func (s *Server) handleGetGuestFileOperation(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	op, err := s.apiStore.GetGuestFileOperation(r.Context(), r.PathValue("operation"))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file operation not found"))
		return
	}
	writeJSON(w, http.StatusOK, publicGuestFileOperation(*op))
}

func (s *Server) handleCancelGuestFileOperation(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	svc := newGuestFileOperationService(s.apiStore)
	op, err := svc.RequestCancel(r.Context(), r.PathValue("operation"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errGuestFileOperationNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, errGuestFileOperationConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	if guestExecutionTerminal(op.Status) {
		writeJSON(w, http.StatusAccepted, publicGuestFileOperation(op))
		return
	}
	if op.CommandID != "" {
		var dispatchErr error
		if op.StartedAt.IsZero() {
			_, dispatchErr = s.agentService().PublishCommand(r.Context(), op.AgentID, controlplane.AgentCommand{ID: op.CommandID, Status: controlplane.AgentCommandStatusCancelled})
		} else {
			_, dispatchErr = s.agentService().PublishCommand(r.Context(), op.AgentID, controlplane.AgentCommand{Type: "cancel_command", Operation: controlplane.NodeOperation{ExternalID: op.ID}})
		}
		if dispatchErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("dispatch file operation cancellation"))
			return
		}
	}
	op, err = svc.Cancel(r.Context(), op.ID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	_ = os.Remove(filepath.Join(s.runsDir, "_guest-files", op.ID))
	_ = os.Remove(filepath.Join(s.runsDir, "_guest-files", op.ID+".json"))
	writeJSON(w, http.StatusAccepted, publicGuestFileOperation(op))
}

func (s *Server) handleStartGuestFileOperation(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	op, err := s.apiStore.GetGuestFileOperation(r.Context(), r.PathValue("operation"))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file operation not found"))
		return
	}
	if op.AgentID != agentID {
		writeError(w, http.StatusForbidden, fmt.Errorf("file operation ownership mismatch"))
		return
	}
	updated, err := newGuestFileOperationService(s.apiStore).MarkRunning(r.Context(), op.ID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, publicGuestFileOperation(updated))
}

func (s *Server) handleCompleteGuestFileOperation(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if err := s.verifyAgentRequest(r, agentID); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	op, err := s.apiStore.GetGuestFileOperation(r.Context(), r.PathValue("operation"))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file operation not found"))
		return
	}
	if op.AgentID != agentID {
		writeError(w, http.StatusForbidden, fmt.Errorf("file operation ownership mismatch"))
		return
	}
	var completion controlplane.GuestFileOperationCompletion
	if err := json.NewDecoder(r.Body).Decode(&completion); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode completion"))
		return
	}
	updated, err := newGuestFileOperationService(s.apiStore).Complete(r.Context(), op.ID, completion.Error)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	_ = os.Remove(filepath.Join(s.runsDir, "_guest-files", op.ID))
	_ = os.Remove(filepath.Join(s.runsDir, "_guest-files", op.ID+".json"))
	writeJSON(w, http.StatusOK, publicGuestFileOperation(updated))
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
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("file payload not found"))
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}
