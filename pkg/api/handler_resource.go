package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
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
	st, err := s.workspaceService().LoadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	health := s.authoritativeTopologyHealth(r.Context(), topology, st)
	writeJSON(w, http.StatusOK, map[string]any{
		"resources": health.Resources,
		"health":    health,
	})
}

func (s *Server) authoritativeTopologyHealth(ctx context.Context, topology string, st *state.State) controlplane.TopologyHealth {
	if proj, ok := latestResourceProjection(s.agents.ListResourceProjections(topology)); ok {
		if len(proj.Resources) > 0 {
			return topologyHealthFromResources(proj.Resources)
		}
		if len(proj.Health.Resources) > 0 {
			return proj.Health
		}
	}
	if st == nil {
		return controlplane.TopologyHealth{Status: controlplane.ResourceHealthUnknown}
	}
	return runtime.EvaluateTopologyHealth(ctx, st)
}

func latestResourceProjection(projections []controlplane.ResourceProjection) (controlplane.ResourceProjection, bool) {
	if len(projections) == 0 {
		return controlplane.ResourceProjection{}, false
	}
	sort.SliceStable(projections, func(i, j int) bool {
		return projections[i].ObservedAt.After(projections[j].ObservedAt)
	})
	return projections[0], true
}

func topologyHealthFromResources(resources []controlplane.ResourceHealth) controlplane.TopologyHealth {
	out := controlplane.TopologyHealth{
		Status:    controlplane.ResourceHealthHealthy,
		Resources: append([]controlplane.ResourceHealth{}, resources...),
	}
	for _, resource := range resources {
		switch resource.Status {
		case controlplane.ResourceHealthHealthy:
			out.Healthy++
		case controlplane.ResourceHealthDrifted:
			out.Drifted++
		default:
			out.Unknown++
		}
	}
	if out.Drifted > 0 {
		out.Status = controlplane.ResourceHealthDrifted
	} else if out.Unknown > 0 {
		out.Status = controlplane.ResourceHealthUnknown
	}
	return out
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
	st, err := s.workspaceService().LoadState(topology)
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
