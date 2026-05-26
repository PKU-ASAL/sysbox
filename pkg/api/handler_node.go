package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
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
	st, err := s.loadState(topology)
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
		if res.Type != "sysbox_node" && res.Type != "sysbox_router" {
			continue
		}
		nodes = append(nodes, nodeInfo{
			Name:      res.Name,
			Provider:  res.Provider,
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
	st, err := s.loadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	res := st.FindResource("sysbox_node", name)
	if res == nil {
		res = st.FindResource("sysbox_router", name)
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

	st, err := s.loadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	res := st.FindResource("sysbox_node", name)
	if res == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("node %q not found", name))
		return
	}

	sub, err := substrate.Get(res.Provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("substrate %q not registered: %w", res.Provider, err))
		return
	}

	if !sub.Capabilities().SupportsPause {
		writeError(w, http.StatusConflict, fmt.Errorf("substrate %q does not support pause/resume", res.Provider))
		return
	}

	subj := s.requestSubject(r)
	required := []string{res.Provider}
	agent, err := s.selectAgent(r.Context(), required)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	op := s.nodeOps.Create(controlplane.NodeOperation{
		Topology:    topology,
		Workspace:   topology,
		Operation:   operation,
		Node:        name,
		Substrate:   res.Provider,
		AgentID:     agent.ID,
		RequestedBy: subj.User,
		Roles:       subj.Roles,
	})
	if _, err := s.publishAgentCommand(r.Context(), agent.ID, controlplane.AgentCommand{
		Type:      "node_operation",
		Operation: op,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
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
	if body.Type != "sysbox_node" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("import only supports sysbox_node, got %q", body.Type))
		return
	}
	if err := validatePathSegment(body.Name, "name"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("id is required"))
		return
	}
	if body.Substrate == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("substrate is required"))
		return
	}

	if _, err := substrate.Get(body.Substrate); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("substrate %q not registered: %w", body.Substrate, err))
		return
	}
	subj := s.requestSubject(r)
	agent, err := s.selectAgent(r.Context(), []string{body.Substrate})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	op := s.nodeOps.Create(controlplane.NodeOperation{
		Topology:    topology,
		Workspace:   topology,
		Operation:   "import",
		Type:        body.Type,
		Name:        body.Name,
		ExternalID:  body.ID,
		Substrate:   body.Substrate,
		AgentID:     agent.ID,
		RequestedBy: subj.User,
		Roles:       subj.Roles,
	})
	if _, err := s.publishAgentCommand(r.Context(), agent.ID, controlplane.AgentCommand{
		Type:      "node_operation",
		Operation: op,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
