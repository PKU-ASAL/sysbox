package api

import (
	"errors"
	"net/http"
	"os"
)

// This endpoint exposes only resource identities, never state attributes/private
// provider data. A missing workspace is not a missing observation endpoint.
func (s *Server) handleCleanupInspection(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, 503, errors.New("state backend unavailable"))
		return
	}
	st, err := mgr.LoadWithContext(r.Context())
	if err != nil {
		writeError(w, 503, errors.New("state inspection unavailable"))
		return
	}
	runs, err := s.apiStore.LoadRuns(r.Context())
	if err != nil {
		writeError(w, 503, errors.New("operation store unavailable"))
		return
	}
	_, err = os.Stat(s.workspaceService().HCLFile(topology))
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, 503, errors.New("metadata inspection unavailable"))
		return
	}
	resources := make([]map[string]any, 0, len(st.Resources))
	for _, resource := range st.Resources {
		resources = append(resources, map[string]any{"resource": resource.Address.String(), "name": resource.Address.Name, "type": resource.ResourceType, "provider": resource.Driver, "external_id": resource.ExternalID, "status": resource.Status})
	}
	active := []map[string]any{}
	for _, run := range latestRunsByID(runs) {
		if run.Topology == topology && run.Status.IsActive() {
			active = append(active, map[string]any{"id": run.ID, "op": run.Op, "operation_key": run.OperationKey, "status": run.Status})
		}
	}
	writeJSON(w, 200, map[string]any{"resources": resources, "active_operations": active, "state_exists": st.Meta.Exists, "metadata_exists": exists})
}
