package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
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

// POST /v1/topologies/{topology}/nodes/{node}/exec
// Body: {"cmd": ["ls", "-la"]}
// Compatibility entry point that now creates an agent-backed console session.
// Attach to /v1/sessions/{session}/attach over WebSocket to stream I/O.
func (s *Server) handleNodeExec(w http.ResponseWriter, r *http.Request) {
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

	var body controlplane.ConsoleRequest
	limitBody(w, r)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Cmd) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("body must be {\"cmd\":[...]}"))
		return
	}
	v := false
	body.TTY = &v
	required, err := requiredCapabilitiesForNode(s.hclFile(topology), name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent, err := s.selectAgent(r.Context(), required)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	sess := s.consoles.Create(topology, name, agent.ID, body)
	if err := s.agents.PublishConsole(agent.ID, sess, body); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sess)
}

// POST /v1/topologies/{topology}/nodes/{node}/pause
func (s *Server) handleNodePause(w http.ResponseWriter, r *http.Request) {
	s.handleNodeLifecycle(r, func(ctx context.Context, sub substrate.Substrate, h substrate.NodeHandle) error {
		return sub.Pause(ctx, h)
	}, w)
}

// POST /v1/topologies/{topology}/nodes/{node}/resume
func (s *Server) handleNodeResume(w http.ResponseWriter, r *http.Request) {
	s.handleNodeLifecycle(r, func(ctx context.Context, sub substrate.Substrate, h substrate.NodeHandle) error {
		return sub.Resume(ctx, h)
	}, w)
}

// handleNodeLifecycle is the shared implementation for pause/resume.
func (s *Server) handleNodeLifecycle(r *http.Request, fn func(context.Context, substrate.Substrate, substrate.NodeHandle) error, w http.ResponseWriter) {
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

	handle, err := res.ReconstructHandle(sub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := fn(r.Context(), sub, handle); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

	sub, err := substrate.Get(body.Substrate)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("substrate %q not registered: %w", body.Substrate, err))
		return
	}

	handle, err := sub.ReadNode(r.Context(), body.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("read node: %w", err))
		return
	}

	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	st, err := mgr.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("load state: %w", err))
		return
	}
	if r := st.FindResource(body.Type, body.Name); r != nil {
		writeError(w, http.StatusConflict, fmt.Errorf("resource %s.%s already in state", body.Type, body.Name))
		return
	}

	inst := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
	}
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		inst["provider_extra"] = string(blob)
	}
	st.AddResource(state.Resource{
		Type:     body.Type,
		Name:     body.Name,
		Provider: body.Substrate,
		Instance: inst,
	})
	runOwner := fmt.Sprintf("sysbox-api:import:%s:%s.%s", topology, body.Type, body.Name)
	if err := mgr.SaveWithLease(r.Context(), st, state.LockOptions{Owner: runOwner}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("save state: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "imported", "id": handle.ID})
}
