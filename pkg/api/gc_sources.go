package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

type gcSourcePublication struct {
	TopologyUID     string                        `json:"topology_uid"`
	ExpectedVersion *int64                        `json:"expected_version"`
	Manifest        *persistence.GCSourceManifest `json:"manifest,omitempty"`
}

func (s *Server) handleGCSources(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	sources, err := repo.LoadGCSources(r.Context(), topology)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, sources)
}
func (s *Server) handleGCSourcePublication(w http.ResponseWriter, r *http.Request) {
	topology, source := r.PathValue("topology"), r.PathValue("source")
	for name, value := range map[string]string{"topology": topology, "source": source} {
		if err := controlstore.ValidatePathSegment(value, name); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	var request gcSourcePublication
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, fmt.Errorf("exactly one JSON object required"))
		return
	}
	if request.TopologyUID == "" || request.ExpectedVersion == nil || *request.ExpectedVersion < 0 {
		writeError(w, 400, fmt.Errorf("topology_uid and nonnegative expected_version required"))
		return
	}
	operation := r.PathValue("operation")
	begin := operation == "begin"
	if (operation != "begin" && operation != "complete" && operation != "retire") || (operation == "complete" && request.Manifest == nil) || (operation != "complete" && request.Manifest != nil) {
		writeError(w, 400, fmt.Errorf("only complete accepts and requires a manifest"))
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	version := *request.ExpectedVersion + 1
	if begin {
		version, err = repo.BeginGCSource(r.Context(), topology, source, request.TopologyUID, *request.ExpectedVersion)
	} else if operation == "retire" {
		err = repo.RetireGCSource(r.Context(), topology, source, request.TopologyUID, *request.ExpectedVersion)
	} else {
		err = repo.CompleteGCSource(r.Context(), topology, source, request.TopologyUID, *request.ExpectedVersion, *request.Manifest)
	}
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]any{"source": source, "version": version, "updating": begin, "retired": operation == "retire"})
}
