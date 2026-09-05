package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/diag"
	"github.com/oslab/sysbox/pkg/runtime"
)

// GET /v1/topologies/{topology}/hcl — return the raw HCL content.
func (s *Server) handleGetHCL(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	data, err := s.workspaceService().HCL(topology)
	if err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// GET /v1/topologies/{topology} — return metadata for a single topology.
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	out, err := s.workspaceService().Get(r.Context(), topology)
	if err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	out.Status = s.topologyStatus(topology)
	if !out.HasState && !out.HasHCL {
		out.Status.Phase = controlplane.PhaseGone
	}

	writeJSON(w, http.StatusOK, out)
}

// topologyStatus projects the readiness conditions of a topology from its
// latest run and most recent health observation. Assertions are not yet
// evaluated (S2 pending), so Asserted is Unknown.
func (s *Server) topologyStatus(topology string) *controlplane.TopologyStatus {
	latest := s.latestRun(topology)
	var assertion *controlplane.AssertionResult
	if latest != nil {
		assertion = latest.Assertion
	}
	status := controlplane.ComputeTopologyStatus(latest, s.healthFor(topology), assertion)
	if latest != nil && isConvergingRun(latest) {
		if deadline := convergenceDeadline(latest, s.cfg.ConvergeTimeout()); !deadline.IsZero() {
			status.DeadlineAt = &deadline
		}
	}
	s.enrichStatus(topology, &status)
	return &status
}

// enrichStatus fills the read-only projections derived from workspace state and
// the HCL output blocks. Both are best-effort: a missing state file or an
// un-evaluable output must not fail the whole status response.
//
// Outputs are evaluated against the synthetic eval context built by
// config.BuildEvalContext (id/name/local/substrate/env only), not against real
// state attributes. An output that references a real state attribute such as
// sysbox_node.web.primary_ip (or an unresolved var.* reference) therefore fails
// to evaluate and is skipped rather than returned. This is consistent with the
// existing GET /v1/topologies/{topology}/outputs endpoint; enriching the eval
// context with real state attributes is a separate follow-up.
func (s *Server) enrichStatus(topology string, status *controlplane.TopologyStatus) {
	// nodes: one entry per sysbox_node resource in state.
	if st, err := s.workspaceService().LoadState(topology); err == nil {
		for _, r := range st.Resources {
			if r.Address.Type != "sysbox_node" {
				continue
			}
			status.Nodes = append(status.Nodes, controlplane.TopologyNode{
				Name:    r.Address.Name,
				Address: r.PrimaryIP(),
				State:   string(r.Status),
			})
		}
	}

	// outputs: evaluate the HCL output blocks against the eval context,
	// tolerating individual failures so one unresolvable output does not blank
	// the rest.
	hclFile := s.workspaceService().HCLFile(topology)
	root, err := config.ParseFile(hclFile)
	if err != nil {
		return
	}
	evalCtx, err := config.BuildEvalContext(root, filepath.Dir(hclFile))
	if err != nil {
		return
	}
	outputs := runtime.EvaluateOutputsBestEffort(root, evalCtx)
	if len(outputs) == 0 {
		return
	}
	status.Outputs = make(map[string]any, len(outputs))
	for k, v := range outputs {
		status.Outputs[k] = v.Value
	}
}

// convergenceDeadline is the instant by which a converging run must have
// finished. It is reported for information; enforcement lives in the
// supervisor, which fails the run when the deadline passes.
func convergenceDeadline(run *controlplane.Run, timeout time.Duration) time.Time {
	if !run.DeadlineAt.IsZero() {
		return run.DeadlineAt
	}
	if timeout <= 0 || run.StartedAt.IsZero() {
		return time.Time{}
	}
	return run.StartedAt.Add(timeout)
}

// isConvergingRun reports whether a run is actively converging: in flight and
// not a destroy.
func isConvergingRun(run *controlplane.Run) bool {
	return run.Op != "destroy" && (run.Status == controlplane.RunQueued || run.Status == controlplane.RunAssigned || run.Status == controlplane.RunRunning)
}

// latestRun returns the most recently started run for a topology, or nil if
// none exist.
func (s *Server) latestRun(topology string) *controlplane.Run {
	var latest *controlplane.Run
	for _, r := range s.jobs.list(topology) {
		if latest == nil || r.StartedAt.After(latest.StartedAt) {
			latest = r
		}
	}
	return latest
}

// healthFor returns the most recent health observation for a topology, or nil
// if the supervisor has not observed it yet.
func (s *Server) healthFor(topology string) *controlplane.TopologyHealth {
	if snap, err := s.loadHealthSnapshot(topology); err == nil && snap != nil {
		return &snap.Health
	}
	return nil
}

// DELETE /v1/topologies/{topology} — remove topology metadata/workspace.
//
// Resource teardown is intentionally handled by POST /destroy. By default this
// endpoint refuses to delete a topology with live state; use ?force=true only
// when the caller intentionally wants to remove metadata/workspace without
// touching external resources.
func (s *Server) handleDeleteTopology(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.workspaceService().Delete(r.Context(), topology, r.URL.Query().Get("force") == "true"); err != nil {
		writeError(w, workspaceStatus(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":    topology,
		"message": "topology deleted",
	})
}

func workspaceStatus(err error) int {
	var diagnostics diag.Diagnostics
	if errors.As(err, &diagnostics) {
		return http.StatusUnprocessableEntity
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already exists"), strings.Contains(msg, "resource(s)"):
		return http.StatusConflict
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no state file"):
		return http.StatusNotFound
	case strings.Contains(msg, "invalid"), strings.Contains(msg, "required"), strings.Contains(msg, "empty HCL"), strings.Contains(msg, "does not support"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
