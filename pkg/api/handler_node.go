package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// GET /v1/topologies/{suite}/nodes
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	st, err := s.loadState(r.PathValue("suite"))
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
			PrimaryIP: util.AsString(res.Instance["primary_ip"]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// GET /v1/topologies/{suite}/nodes/{node}
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	suite := r.PathValue("suite")
	name := r.PathValue("node")
	st, err := s.loadState(suite)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	res := st.FindResource("sysbox_node", name)
	if res == nil {
		res = st.FindResource("sysbox_router", name)
	}
	if res == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("node %q not found in suite %q", name, suite))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// POST /v1/topologies/{suite}/nodes/{node}/exec
// Body: {"cmd": ["ls", "-la"]}
// Response: chunked plain text (stdout+stderr interleaved)
func (s *Server) handleNodeExec(w http.ResponseWriter, r *http.Request) {
	suite := r.PathValue("suite")
	name := r.PathValue("node")

	var body struct {
		Cmd []string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Cmd) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("body must be {\"cmd\":[...]}"))
		return
	}

	st, err := s.loadState(suite)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	conn, err := nodeConnection(st, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := conn.ExecStream(r.Context(), body.Cmd, w, w); err != nil {
		fmt.Fprintf(w, "\nerror: %v\n", err)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// nodeConnection rebuilds a substrate.Connection from persisted state.
// Works for Docker (container_id) and Firecracker (provider_extra SSH info).
func nodeConnection(st *state.State, name string) (substrate.Connection, error) {
	res := st.FindResource("sysbox_node", name)
	if res == nil {
		res = st.FindResource("sysbox_router", name)
	}
	if res == nil {
		return nil, fmt.Errorf("node %q not in state", name)
	}

	sub, err := substrate.Get(res.Provider)
	if err != nil {
		return nil, fmt.Errorf("substrate %q not registered: %w", res.Provider, err)
	}

	// Reconstruct provider-specific handle state from the persisted blob.
	var providerState any
	if blob := util.AsString(res.Instance["provider_extra"]); blob != "" {
		providerState, err = sub.UnmarshalProviderState([]byte(blob))
		if err != nil {
			return nil, fmt.Errorf("node %q: corrupt provider state: %w", name, err)
		}
	}

	handle := substrate.NodeHandle{
		ID:       util.AsString(res.Instance["container_id"]),
		Net:      substrate.NetInfo{PrimaryIP: util.AsString(res.Instance["primary_ip"])},
		Provider: providerState,
	}

	conn, err := sub.Connection(handle, nil)
	if err != nil || conn == nil {
		return nil, fmt.Errorf("no connection to node %q: %w", name, err)
	}
	return conn, nil
}

// POST /v1/topologies/{suite}/nodes/{node}/pause
func (s *Server) handleNodePause(w http.ResponseWriter, r *http.Request) {
	s.handleNodeLifecycle(r, func(ctx context.Context, sub substrate.Substrate, h substrate.NodeHandle) error {
		return sub.Pause(ctx, h)
	}, w)
}

// POST /v1/topologies/{suite}/nodes/{node}/resume
func (s *Server) handleNodeResume(w http.ResponseWriter, r *http.Request) {
	s.handleNodeLifecycle(r, func(ctx context.Context, sub substrate.Substrate, h substrate.NodeHandle) error {
		return sub.Resume(ctx, h)
	}, w)
}

// handleNodeLifecycle is the shared implementation for pause/resume.
func (s *Server) handleNodeLifecycle(r *http.Request, fn func(context.Context, substrate.Substrate, substrate.NodeHandle) error, w http.ResponseWriter) {
	suite := r.PathValue("suite")
	name := r.PathValue("node")

	st, err := s.loadState(suite)
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

	handle := substrate.NodeHandle{
		ID:  util.AsString(res.Instance["container_id"]),
		Net: substrate.NetInfo{PrimaryIP: util.AsString(res.Instance["primary_ip"])},
	}
	if blob := util.AsString(res.Instance["provider_extra"]); blob != "" {
		if ps, err := sub.UnmarshalProviderState([]byte(blob)); err == nil {
			handle.Provider = ps
		}
	}

	if err := fn(r.Context(), sub, handle); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/topologies/{suite}/import
// Body: {"type": "sysbox_node", "name": "db", "id": "my-container", "substrate": "docker"}
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	suite := r.PathValue("suite")

	var body struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		ID        string `json:"id"`
		Substrate string `json:"substrate"`
	}
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

	stateFile := s.stateFile(suite)
	mgr := state.NewManager(stateFile)
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
	if err := mgr.Save(st); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("save state: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "imported", "id": handle.ID})
}
