package api

import (
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
)

// GET /v1/capabilities
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	res := runtime.PreflightResult{OK: true}
	for _, name := range []string{"docker", "firecracker", "libvirt"} {
		if nodeDriver, err := driver.DefaultRegistry.RequireNode(name); err == nil {
			runtime.AddPreflightChecks(&res, nodeDriver.PreflightChecks(false))
		}
	}
	writeJSON(w, http.StatusOK, s.runtimeCapabilities(res))
}

// GET /v1/topologies/{topology}/preflight
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.preflightTopology(topology)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) preflightTopology(topology string) (*runtime.PreflightResult, error) {
	return runtime.PreflightFile(s.workspaceService().HCLFile(topology), driver.DefaultRegistry)
}
