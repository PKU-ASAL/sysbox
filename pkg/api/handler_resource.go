package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// GET /v1/topologies/{topology}/resources
func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
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

func (s *Server) authoritativeTopologyHealth(ctx context.Context, topology string, st *state.State) model.TopologyHealth {
	projection, projectionErr := s.apiStore.LoadResourceProjection(ctx, topology)
	if projectionErr == nil && projection != nil && !controlplane.FreshHostObservation(projection.ObservedAt, time.Now()) {
		if st == nil {
			return model.TopologyHealth{Status: model.ResourceHealthUnknown}
		}
		return topologyHealthFromResources(pendingStateResources(st))
	}
	usesVM := stateHasVM(st) || (projectionErr == nil && projectionHasVM(projection))
	if usesVM {
		// VM agent observations can outlive a guest/agent restart. Trust only a snapshot
		// that matches the current VM state and agent process; otherwise report pending.
		if st == nil || len(st.Resources) == 0 {
			return topologyHealthFromResources(nil)
		}
		if projectionErr == nil && projection != nil {
			resources := projection.Resources
			if len(resources) == 0 {
				resources = projection.Health.Resources
			}
			if resourceProjectionMatchesState(projection, resources, st) {
				return topologyHealthFromResources(resources)
			}
			return topologyHealthFromResources(pendingStateResources(st))
		}
		return runtime.EvaluateTopologyHealth(ctx, st, driver.DefaultRegistry)
	}

	// Keep the original Docker health projection semantics unchanged.
	if projectionErr == nil && projection != nil {
		if len(projection.Resources) > 0 {
			return topologyHealthFromResources(projection.Resources)
		}
		if len(projection.Health.Resources) > 0 {
			return projection.Health
		}
	}
	if st == nil {
		return model.TopologyHealth{Status: model.ResourceHealthUnknown}
	}
	return runtime.EvaluateTopologyHealth(ctx, st, driver.DefaultRegistry)
}

func stateHasVM(st *state.State) bool {
	if st == nil {
		return false
	}
	for _, resource := range st.Resources {
		if resource.Driver == "firecracker" || resource.Driver == "libvirt" {
			return true
		}
	}
	return false
}

func projectionHasVM(projection *controlplane.ResourceProjection) bool {
	if projection == nil {
		return false
	}
	for _, resource := range append(append([]model.ResourceHealth{}, projection.Resources...), projection.Health.Resources...) {
		if resource.Provider == "firecracker" || resource.Provider == "libvirt" {
			return true
		}
	}
	return false
}

func resourceProjectionMatchesState(proj *controlplane.ResourceProjection, resources []model.ResourceHealth, st *state.State) bool {
	if proj.Serial != st.Meta.Serial || len(resources) != len(st.Resources) {
		return false
	}
	byID := make(map[string]model.ResourceHealth, len(resources))
	for _, resource := range resources {
		if _, duplicate := byID[resource.Resource]; duplicate {
			return false
		}
		byID[resource.Resource] = resource
	}
	for _, resource := range st.Resources {
		observed, ok := byID[resource.Address.String()]
		if !ok || observed.Type != resource.Address.Type || observed.Name != resource.Address.Name || observed.Provider != resource.Driver {
			return false
		}
		if id := stateResourceExternalID(resource); (resource.Address.Type == "node" || resource.Address.Type == "sysbox_node") && id != "" {
			if observed.Observation == nil || observed.Observation.ExternalID == "" {
				// Legacy unversioned agents did not always send observations.
				// Keep that contract only for serial-zero state; versioned state
				// requires an identity, and an explicit mismatch is never accepted.
				if proj.Serial != 0 {
					return false
				}
			} else if observed.Observation.ExternalID != id {
				return false
			}
		}
	}
	return true
}

func stateResourceExternalID(resource state.Resource) string {
	if resource.ExternalID != "" {
		return resource.ExternalID
	}
	if handler, ok := runtime.GetResourceHandler(resource.Address.Type); ok {
		return handler.ExternalID(resource)
	}
	return ""
}

func pendingStateResources(st *state.State) []model.ResourceHealth {
	out := make([]model.ResourceHealth, 0, len(st.Resources))
	for _, resource := range st.Resources {
		out = append(out, model.ResourceHealth{
			Resource: resource.Address.String(), Type: resource.Address.Type,
			Name: resource.Address.Name, Provider: resource.Driver,
			Status:   model.ResourceHealthUnknown,
			Reason:   "waiting for an agent observation matching current state",
			Decision: model.RecoveryDecisionUnknown,
		})
	}
	return out
}

func topologyHealthFromResources(resources []model.ResourceHealth) model.TopologyHealth {
	out := model.TopologyHealth{
		Status:    model.ResourceHealthHealthy,
		Resources: append([]model.ResourceHealth{}, resources...),
	}
	for _, resource := range resources {
		switch resource.Status {
		case model.ResourceHealthHealthy:
			out.Healthy++
		case model.ResourceHealthDrifted:
			out.Drifted++
		default:
			out.Unknown++
		}
	}
	if out.Drifted > 0 {
		out.Status = model.ResourceHealthDrifted
	} else if out.Unknown > 0 {
		out.Status = model.ResourceHealthUnknown
	}
	return out
}

func (s *Server) handleTopologyStatusStream(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
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
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
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
	writeJSON(w, http.StatusOK, runtime.EvaluateResourceHealth(r.Context(), res, driver.DefaultRegistry))
}

func validateResourceSegment(resource string) error {
	typ, name, ok := strings.Cut(resource, ".")
	if !ok || typ == "" || name == "" {
		return fmt.Errorf("invalid resource %q: expected type.name", resource)
	}
	if err := controlstore.ValidatePathSegment(typ, "resource type"); err != nil {
		return err
	}
	return controlstore.ValidatePathSegment(name, "resource name")
}

func findResourceByID(st *state.State, id string) (*state.Resource, error) {
	addr, err := address.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid resource %q", id)
	}
	res := st.FindResource(addr)
	if res == nil {
		return nil, fmt.Errorf("resource %q not found", id)
	}
	return res, nil
}
