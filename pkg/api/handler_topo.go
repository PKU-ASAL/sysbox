package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/oslab/sysbox/pkg/diag"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// safeSegment matches allowed path values for topology / node / id URL segments.
// Rejects "..", special characters, and URL-encoded traversals.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validatePathSegment returns an error if the segment could enable path traversal.
func validatePathSegment(seg, label string) error {
	if !safeSegment.MatchString(seg) {
		return fmt.Errorf("invalid %s %q: must match [A-Za-z0-9_-]+", label, seg)
	}
	return nil
}

// GET /v1/health
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /v1/topologies
// Returns the union of topologies discovered under workspacesDir (HCL present)
// and runsDir (state file present). Each topology carries flags indicating
// whether it has been applied yet.
func (s *Server) handleListTopologies(w http.ResponseWriter, r *http.Request) {
	out, err := s.workspaceService().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topologies": out})
}

// GET /v1/topologies/{topology}/state
func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGetStateMetadata(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	meta, err := s.workspaceService().Metadata(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleGetStateLock(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := s.workspaceService().LockInfo(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleForceUnlockState(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.workspaceService().ForceUnlock(r.Context(), topology); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unlocked"})
}

func (s *Server) handleListStateSnapshots(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.workspaceService().StateSnapshots(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": items})
}

func (s *Server) handleRestoreStateSnapshot(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	snapshot := r.PathValue("snapshot")
	if err := validatePathSegment(snapshot, "snapshot"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.workspaceService().RestoreStateSnapshot(r.Context(), topology, snapshot); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "snapshot": snapshot})
}

func (s *Server) handleGetOutputs(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	_, _, _, root, evalCtx, err := runtime.LoadWorkspaceWithManager(s.workspaceService().HCLFile(topology), state.NewManager(s.workspaceService().StateFile(topology)))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	outputs, err := runtime.EvaluateOutputs(root, evalCtx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := r.URL.Query().Get("name")
	if name != "" {
		out, ok := outputs[name]
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("output %q not found", name))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"outputs": map[string]runtime.OutputValue{name: out}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outputs": outputs})
}

// GET /v1/topologies/{topology}/health
func (s *Server) handleGetTopologyHealth(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if r.URL.Query().Get("cached") == "true" {
		snap, err := s.loadHealthSnapshot(topology)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
		return
	}
	st, err := s.workspaceService().LoadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	health := s.authoritativeTopologyHealth(r.Context(), topology, st)
	writeJSON(w, http.StatusOK, health)
}

// GET /v1/topologies/{topology}/plan
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	g, _, st, _, _, err := runtime.LoadWorkspaceWithManager(s.workspaceService().HCLFile(topology), mgr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runtime.NewExecutor(g, st).Refresh(r.Context(), plan)
	writeJSON(w, http.StatusOK, planJSON(plan))
}

// POST /v1/topologies/{topology}/apply
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req, err := decodeApplyRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.runs().StartApply(r.Context(), topology, RunStartRequest(req))
	if err != nil {
		writeError(w, runServiceStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "agent_id": run.AgentID})
}

// POST /v1/topologies/{topology}/repair
func (s *Server) handleRepair(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req, err := decodeApplyRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.runs().StartRepair(r.Context(), topology, RunStartRequest(req))
	if err != nil {
		writeError(w, runServiceStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "agent_id": run.AgentID, "operation": "repair"})
}

type applyRequest struct {
	PlanID   string `json:"plan_id"`
	Revision string `json:"revision,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
}

func decodeApplyRequest(r *http.Request) (applyRequest, error) {
	if r.Body == nil || r.ContentLength == 0 {
		return applyRequest{}, nil
	}
	defer r.Body.Close()
	var req applyRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return applyRequest{}, fmt.Errorf("decode apply request: %w", err)
	}
	if req.PlanID != "" {
		if err := validatePathSegment(req.PlanID, "plan_id"); err != nil {
			return applyRequest{}, err
		}
	}
	if req.Revision != "" {
		if err := validatePathSegment(req.Revision, "revision"); err != nil {
			return applyRequest{}, err
		}
	}
	if req.AgentID != "" {
		if err := validatePathSegment(req.AgentID, "agent_id"); err != nil {
			return applyRequest{}, err
		}
	}
	return req, nil
}

func writePreflightLogsTo(w io.Writer, res *preflightResult) {
	if res == nil {
		return
	}
	_, _ = w.Write([]byte("Preflight checks:\n"))
	for _, c := range res.Checks {
		status := "ok"
		if !c.OK {
			status = c.Severity
		}
		line := fmt.Sprintf("  [%s] %s", status, c.Name)
		if c.Message != "" {
			line += ": " + c.Message
		}
		if c.Hint != "" {
			line += " (" + c.Hint + ")"
		}
		line += "\n"
		_, _ = w.Write([]byte(line))
	}
}

// POST /v1/topologies/{topology}/destroy
func (s *Server) handleDestroy(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.runs().StartDestroy(r.Context(), topology)
	if err != nil {
		writeError(w, runServiceStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "agent_id": run.AgentID})
}

// GET /v1/runs/{id}
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	topology := r.URL.Query().Get("topology")
	if topology != "" {
		if err := validatePathSegment(topology, "topology"); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.jobs.list(topology)})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, parent, err := s.runs().Resume(r.Context(), id)
	if err != nil {
		writeError(w, runServiceStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "parent_id": parent.ID, "agent_id": run.AgentID})
}

func (s *Server) reconcileParentJournal(parent, run *controlplane.Run) error {
	mgr, err := s.stateManager(parent.Topology)
	if err != nil {
		return err
	}
	report, err := reconcileCheckpointJournal(context.Background(), s.apiStore, parent.Topology, parent.ID, mgr, run.LeaseOwner)
	if err != nil {
		return err
	}
	if report == nil {
		return nil
	}
	logs := s.jobs.logWriter(run.ID)
	for _, action := range report.Recovered {
		_, _ = logs.Write([]byte(fmt.Sprintf("[resume] %s %s", action.Status, action.Resource)))
		if action.ExternalID != "" {
			_, _ = logs.Write([]byte(fmt.Sprintf(" (%s)", action.ExternalID)))
		}
		_, _ = logs.Write([]byte("\n"))
	}
	for _, action := range report.Skipped {
		_, _ = logs.Write([]byte(fmt.Sprintf("[resume] skipped %s: %s\n", action.Resource, action.Status)))
	}
	return nil
}

func (s *Server) handleCleanupRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	if run.Status == controlplane.RunRunning {
		writeError(w, http.StatusConflict, fmt.Errorf("run %s is still running", id))
		return
	}
	report, err := cleanupCheckpoint(r.Context(), s.apiStore, run.Topology, run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleRecoverRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	if run.Status == controlplane.RunRunning {
		writeError(w, http.StatusConflict, fmt.Errorf("run %s is still running", id))
		return
	}
	mgr, err := s.stateManager(run.Topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	owner := fmt.Sprintf("sysbox-api:recover:%s", run.ID)
	report, err := reconcileCheckpointJournal(r.Context(), s.apiStore, run.Topology, run.ID, mgr, owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleGetRunCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	cp, err := s.apiStore.LoadCheckpoint(r.Context(), run.Topology, run.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
		return
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleGetRunActions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	cp, err := s.apiStore.LoadCheckpoint(r.Context(), run.Topology, run.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, runActionLogFromCheckpoint(*cp))
}

// GET /v1/runs/{id}/logs  — SSE stream
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	logs := s.jobs.logWriter(run.ID)
	ch := logs.Subscribe()
	defer logs.Unsubscribe(ch)
	ServeSSE(w, r, ch)
}

// helpers

func (s *Server) checkpointFile(topology, runID string) string {
	return filepath.Join(s.runsDir, topology, "runs", runID+".checkpoint.json")
}

func planJSON(p *runtime.Plan) map[string]any {
	add := make([]string, 0, len(p.Add))
	for _, id := range p.Add {
		add = append(add, id.String())
	}
	destroy := make([]string, 0, len(p.Destroy))
	for _, r := range p.Destroy {
		destroy = append(destroy, r.Address.String())
	}
	change := make([]string, 0, len(p.Change))
	for _, id := range p.Change {
		change = append(change, id.String())
	}
	return map[string]any{
		"summary": p.Summary(),
		"add":     add,
		"destroy": destroy,
		"change":  change,
		"actions": p.Actions,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("writeJSON encode failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	var diagnostics diag.Diagnostics
	if errors.As(err, &diagnostics) {
		writeJSON(w, status, map[string]any{"error": err.Error(), "diagnostics": diagnostics})
		return
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func containsAny(s string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(s, value) {
			return true
		}
	}
	return false
}
