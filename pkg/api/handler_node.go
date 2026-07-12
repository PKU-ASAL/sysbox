package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/address"
)

// maxBodyBytes limits request body size to 1MB across all API handlers
// to prevent OOM from oversized JSON payloads.
const maxBodyBytes = 1 << 20

// limitBody wraps r.Body with an http.MaxBytesReader to prevent OOM.
func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
}

// GET /v1/topologies/{topology}/nodes
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.workspaceService().LoadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	type nodeInfo struct {
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		PrimaryIP string `json:"primary_ip,omitempty"`
	}
	var nodes []nodeInfo
	for _, res := range st.Resources {
		if res.Address.Type != "sysbox_node" && res.Address.Type != "sysbox_router" {
			continue
		}
		nodes = append(nodes, nodeInfo{
			Name:      res.Address.Name,
			Provider:  res.Driver,
			PrimaryIP: res.PrimaryIP(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// GET /v1/topologies/{topology}/nodes/{node}
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	name := r.PathValue("node")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(name, "node"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.workspaceService().LoadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	res := st.FindResource(address.Resource("sysbox_node", name))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", name))
	}
	if res == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("node %q not found in topology %q", name, topology))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// POST /v1/topologies/{topology}/nodes/{node}/pause
func (s *Server) handleNodePause(w http.ResponseWriter, r *http.Request) {
	s.handleNodeLifecycle(r, "pause", w)
}

// POST /v1/topologies/{topology}/nodes/{node}/resume
func (s *Server) handleNodeResume(w http.ResponseWriter, r *http.Request) {
	s.handleNodeLifecycle(r, "resume", w)
}

// handleNodeLifecycle is the shared implementation for pause/resume.
func (s *Server) handleNodeLifecycle(r *http.Request, operation string, w http.ResponseWriter) {
	topology := r.PathValue("topology")
	name := r.PathValue("node")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(name, "node"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	op, err := s.nodeOperations().Lifecycle(r.Context(), topology, name, operation, s.requestSubject(r))
	if err != nil {
		writeError(w, nodeOperationStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// POST /v1/topologies/{topology}/import
// Body: {"type": "sysbox_node", "name": "db", "id": "my-container", "substrate": "docker"}
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var body struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		ID        string `json:"id"`
		Substrate string `json:"substrate"`
	}
	limitBody(w, r)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	op, err := s.nodeOperations().Import(r.Context(), topology, ImportNodeRequest{
		Type:      body.Type,
		Name:      body.Name,
		ID:        body.ID,
		Substrate: body.Substrate,
	}, s.requestSubject(r))
	if err != nil {
		writeError(w, nodeOperationStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func nodeOperationStatus(err error) int {
	msg := err.Error()
	switch {
	case containsAny(msg, "not found"):
		return http.StatusNotFound
	case containsAny(msg, "required", "invalid", "only supports", "not registered"):
		return http.StatusBadRequest
	case containsAny(msg, "does not support", "no online agent", "does not satisfy"):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
