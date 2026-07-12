package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/oslab/sysbox/pkg/diag"
)

// POST /v1/topologies — create a new topology workspace.
//
// Two request modes:
//   - JSON body:     {"name":"my-lab","hcl":"resource ..."}  — name embedded
//   - Raw text body: HCL text, name from ?name= query param  — CLI-friendly
//
// The HCL is validated before persisting. Returns 201 on success.
// If the topology already exists, returns 409 Conflict.
func (s *Server) handleCreateTopology(w http.ResponseWriter, r *http.Request) {
	var name, hcl string

	contentType := r.Header.Get("Content-Type")

	if contentType == "application/json" {
		// JSON mode: name + hcl in one body.
		limitBody(w, r)
		var body struct {
			Name string `json:"name"`
			HCL  string `json:"hcl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
			return
		}
		name = body.Name
		hcl = body.HCL
	} else {
		// Raw text mode: name from query param, HCL is the body.
		name = r.URL.Query().Get("name")
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
			return
		}
		hcl = string(data)
	}

	if err := validatePathSegment(name, "name"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if hcl == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("hcl is required"))
		return
	}

	info, err := s.workspaceService().Create(r.Context(), name, hcl)
	if err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	writeJSON(w, http.StatusCreated, info)
}

// PUT /v1/topologies/{topology}/hcl — replace the HCL content of an existing topology.
//
// Request body is raw HCL text. Validates before persisting.
func (s *Server) handleUpdateHCL(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Hold topology lock so we don't race with an ongoing apply/destroy.
	unlock := s.jobs.lockTopology(topology)
	defer unlock()

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	hclBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
		return
	}
	if err := s.workspaceService().UpdateHCL(r.Context(), topology, hclBytes); err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":    topology,
		"message": "HCL updated",
	})
}

// GET /v1/topologies/{topology}/hcl — return the raw HCL content.
func (s *Server) handleGetHCL(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	data, err := s.workspaceService().HCL(topology)
	if err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// GET /v1/topologies/{topology} — return metadata for a single topology.
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	out, err := s.workspaceService().Get(r.Context(), topology)
	if err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

// DELETE /v1/topologies/{topology} — remove topology metadata/workspace.
//
// Resource teardown is intentionally handled by POST /destroy. By default this
// endpoint refuses to delete a topology with live state; use ?force=true only
// when the caller intentionally wants to remove metadata/workspace without
// touching external resources.
func (s *Server) handleDeleteTopology(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.workspaceService().Delete(r.Context(), topology, r.URL.Query().Get("force") == "true"); err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":    topology,
		"message": "topology deleted",
	})
}

func workspaceStatus(err error) int {
	var diagnostics diag.Diagnostics
	if errors.As(err, &diagnostics) {
		return http.StatusUnprocessableEntity
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already exists"), strings.Contains(msg, "resource(s)"):
		return http.StatusConflict
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no state file"):
		return http.StatusNotFound
	case strings.Contains(msg, "invalid"), strings.Contains(msg, "required"), strings.Contains(msg, "empty HCL"), strings.Contains(msg, "does not support"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
