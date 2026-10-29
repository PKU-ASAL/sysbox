package api

import (
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
)

// Preview reads the selected immutable configuration and canonical state. It
// does not admit execution or claim to have performed live provider refresh.
func (s *Server) handleRevisionPlan(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	request, err := decodeApplyRequest(r)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	if !controlstore.GlobalRevisionPattern.MatchString(request.Revision) {
		writeError(w, 400, fmt.Errorf("plan requires an immutable revision"))
		return
	}
	revision, err := s.apiStore.GetGlobalRevision(r.Context(), request.Revision)
	if err != nil {
		writeError(w, 404, err)
		return
	}
	snapshot, err := s.workspaceService().MaterializeRevision(topology, request.Revision, revision.Files)
	if err != nil {
		writeError(w, runServiceStatus(err), err)
		return
	}
	manager, err := s.stateManager(topology)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	graph, _, state, _, _, err := runtime.LoadWorkspaceWithInputsContext(r.Context(), snapshot, manager, request.Inputs, driver.DefaultRegistry)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	plan, err := runtime.ComputePlan(graph, state, driver.DefaultRegistry)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	if request.Target != "" {
		target, err := address.Parse(request.Target)
		if err != nil || !target.Equal(address.Resource(target.Type, target.Name)) {
			writeError(w, 400, fmt.Errorf("invalid target"))
			return
		}
		plan = runtime.FilterPlanByTarget(plan, target.Type, target.Name)
	}
	writeJSON(w, 200, planJSON(plan))
}
