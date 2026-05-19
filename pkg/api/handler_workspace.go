package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
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

	// Validate HCL before persisting.
	if _, err := config.ParseString(hcl, ".hcl"); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid HCL: %w", err))
		return
	}

	dir := filepath.Join(s.workspacesDir, name)
	hclPath := filepath.Join(dir, "field.sysbox.hcl")

	if _, err := os.Stat(hclPath); err == nil {
		writeError(w, http.StatusConflict, fmt.Errorf("topology %q already exists", name))
		return
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create directory: %w", err))
		return
	}
	if err := os.WriteFile(hclPath, []byte(hcl), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write hcl: %w", err))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"name":      name,
		"has_hcl":   true,
		"has_state": false,
	})
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

	hclPath := s.hclFile(topology)
	if _, err := os.Stat(hclPath); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("topology %q not found", topology))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	hclBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
		return
	}
	if len(hclBytes) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("empty HCL"))
		return
	}

	if _, err := config.ParseString(string(hclBytes), ".hcl"); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid HCL: %w", err))
		return
	}

	if err := os.WriteFile(hclPath, hclBytes, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write hcl: %w", err))
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

	hclPath := s.hclFile(topology)
	data, err := os.ReadFile(hclPath)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("topology %q not found", topology))
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

	out := map[string]any{
		"name":      topology,
		"has_hcl":   false,
		"has_state": false,
	}

	if _, err := os.Stat(s.hclFile(topology)); err == nil {
		out["has_hcl"] = true
	}
	if _, err := os.Stat(s.stateFile(topology)); err == nil {
		out["has_state"] = true
		// Load state to enrich with resource counts.
		if st, err := s.loadState(topology); err == nil {
			out["resource_count"] = len(st.Resources)
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// DELETE /v1/topologies/{topology} — remove a topology entirely.
//
// If the topology has been applied (state has resources), it is destroyed
// automatically before files are removed. Use ?force=true to skip the
// destroy step (orphans running containers/VMs).
func (s *Server) handleDeleteTopology(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// If state has resources, auto-destroy unless ?force=true.
	statePath := s.stateFile(topology)
	if _, err := os.Stat(statePath); err == nil {
		mgr := state.NewManager(statePath)
		st, err := mgr.Load()
		if err == nil && len(st.Resources) > 0 {
			if r.URL.Query().Get("force") == "true" {
				slog.Warn("force-deleting topology with live resources", "topology", topology, "resources", len(st.Resources))
			} else {
				// Auto-destroy: run destroy inline (synchronous).
				plan := &runtime.Plan{Destroy: append([]state.Resource(nil), st.Resources...)}
				exec := runtime.NewExecutor(graph.New(), st)
				if err := exec.Destroy(context.Background(), plan); err != nil {
					writeError(w, http.StatusConflict, fmt.Errorf("auto-destroy failed: %w", err))
					return
				}
				slog.Info("auto-destroyed topology before deletion", "topology", topology)
			}
		}
		// Remove state directory.
		if err := os.RemoveAll(filepath.Dir(statePath)); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("remove state: %w", err))
			return
		}
	}

	// Remove workspace directory.
	wsDir := filepath.Join(s.workspacesDir, topology)
	if err := os.RemoveAll(wsDir); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("remove workspace: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":    topology,
		"message": "topology deleted",
	})
}
