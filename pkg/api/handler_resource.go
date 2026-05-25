package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// GET /v1/topologies/{topology}/resources
func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
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
	health := runtime.EvaluateTopologyHealth(r.Context(), st)
	writeJSON(w, http.StatusOK, map[string]any{
		"resources":   health.Resources,
		"projections": s.agents.ListResourceProjections(topology),
	})
}

func (s *Server) handleTopologyStatusStream(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	stream := s.agents.StatusStream(topology)
	ch := stream.Subscribe()
	defer stream.Unsubscribe(ch)
	ServeSSE(w, r, ch)
}

// GET /v1/topologies/{topology}/resources/{type.name}/health
func (s *Server) handleGetResourceHealth(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	resource := r.PathValue("resource")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateResourceSegment(resource); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.loadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	res, err := findResourceByID(st, resource)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, runtime.EvaluateResourceHealth(r.Context(), res))
}

func validateResourceSegment(resource string) error {
	typ, name, ok := strings.Cut(resource, ".")
	if !ok || typ == "" || name == "" {
		return fmt.Errorf("invalid resource %q: expected type.name", resource)
	}
	if err := validatePathSegment(typ, "resource type"); err != nil {
		return err
	}
	return validatePathSegment(name, "resource name")
}

func findResourceByID(st *state.State, id string) (*state.Resource, error) {
	typ, name, ok := strings.Cut(id, ".")
	if !ok {
		return nil, fmt.Errorf("invalid resource %q", id)
	}
	res := st.FindResource(typ, name)
	if res == nil {
		return nil, fmt.Errorf("resource %q not found", id)
	}
	return res, nil
}
