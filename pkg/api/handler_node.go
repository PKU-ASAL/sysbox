package api

import (
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
		providerState, _ = sub.UnmarshalProviderState([]byte(blob))
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
