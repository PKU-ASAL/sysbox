package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"

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
	type topologyInfo struct {
		ArtifactID    string `json:"artifact_id"`
		TopologyID    string `json:"topology_id,omitempty"`
		Name          string `json:"name"`
		HasHCL        bool   `json:"has_hcl"`
		HasState      bool   `json:"has_state"`
		ResourceCount int    `json:"resource_count,omitempty"`
		Serial        int64  `json:"serial,omitempty"`
		Backend       string `json:"backend,omitempty"`
	}
	topolist := map[string]*topologyInfo{}

	hclEntries, err := filepath.Glob(filepath.Join(s.workspacesDir, "*", "field.sysbox.hcl"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, e := range hclEntries {
		name := filepath.Base(filepath.Dir(e))
		topolist[name] = &topologyInfo{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name, HasHCL: true}
	}

	if s.stateBackend == "" {
		stateEntries, err := filepath.Glob(filepath.Join(s.runsDir, "*", "state.json"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, e := range stateEntries {
			name := filepath.Base(filepath.Dir(e))
			info := topolist[name]
			if info == nil {
				info = &topologyInfo{ArtifactID: artifactID(name), TopologyID: topologyID(name), Name: name}
				topolist[name] = info
			}
			info.HasState = true
			info.Backend = "local"
		}
	} else {
		mgr, err := s.stateManager("__list__")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items, err := mgr.ListTopologies(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, item := range items {
			if err := validatePathSegment(item.Name, "topology"); err != nil {
				continue
			}
			info := topolist[item.Name]
			if info == nil {
				info = &topologyInfo{ArtifactID: artifactID(item.Name), TopologyID: topologyID(item.Name), Name: item.Name}
				topolist[item.Name] = info
			}
			info.ArtifactID = artifactID(item.Name)
			info.TopologyID = topologyID(item.Name)
			info.HasState = item.HasState
			info.ResourceCount = item.ResourceCount
			info.Serial = item.Serial
			info.Backend = item.Backend
		}
	}

	out := make([]topologyInfo, 0, len(topolist))
	for _, info := range topolist {
		out = append(out, *info)
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
	st, err := s.loadState(topology)
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
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	meta, err := mgr.Metadata(r.Context())
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
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	info, err := mgr.LockInfo(r.Context())
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
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := mgr.ForceUnlock(r.Context()); err != nil {
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
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	backend := mgr.Backend()
	snapshots, ok := backend.(state.SnapshotBackend)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"snapshots": []state.Snapshot{}})
		return
	}
	items, err := snapshots.ListSnapshots(r.Context())
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
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	backend := mgr.Backend()
	snapshots, ok := backend.(state.SnapshotBackend)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("state backend does not support snapshots"))
		return
	}
	if err := snapshots.RestoreSnapshot(r.Context(), snapshot); err != nil {
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
	_, _, _, root, evalCtx, err := runtime.LoadWorkspaceWithManager(s.hclFile(topology), state.NewManager(s.stateFile(topology)))
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
	st, err := s.loadState(topology)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	health := runtime.EvaluateTopologyHealth(r.Context(), st)
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
	g, _, st, _, _, err := runtime.LoadWorkspaceWithManager(s.hclFile(topology), mgr)
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
	if req.PlanID != "" {
		currentSerial, err := s.currentStateSerial(r.Context(), topology)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		plan, err := s.validateStoredPlanForApply(r.Context(), topology, req.PlanID, currentSerial)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		req.Revision = plan.Revision
	}
	run := s.jobs.startWithOptions(topology, "apply", runStartOptions{
		Revision: req.Revision,
		PlanID:   req.PlanID,
	})
	required, err := requiredCapabilitiesForTopology(s.hclFile(topology))
	if err != nil {
		s.jobs.finish(run, err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.dispatchRun(r.Context(), run, required); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "agent_id": run.AgentID})
}

type applyRequest struct {
	PlanID   string `json:"plan_id"`
	Revision string `json:"revision,omitempty"`
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
	return req, nil
}

func (s *Server) currentStateSerial(ctx context.Context, topology string) (int64, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return 0, err
	}
	meta, err := mgr.Metadata(ctx)
	if err != nil {
		return 0, err
	}
	return meta.Serial, nil
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
	run := s.jobs.start(topology, "destroy")
	required, err := requiredCapabilitiesForTopology(s.hclFile(topology))
	if err != nil {
		s.jobs.finish(run, err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.dispatchRun(r.Context(), run, required); err != nil {
		writeError(w, http.StatusConflict, err)
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
	parent, ok := s.jobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	if parent.Status == RunRunning {
		writeError(w, http.StatusConflict, fmt.Errorf("run %s is still running", id))
		return
	}
	if parent.Op != "apply" && parent.Op != "destroy" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("run op %q cannot be resumed", parent.Op))
		return
	}
	run := s.jobs.startChild(parent)
	switch run.Op {
	case "apply":
		required, err := requiredCapabilitiesForTopology(s.hclFile(run.Topology))
		if err != nil {
			s.jobs.finish(run, err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.dispatchRun(r.Context(), run, required); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
	case "destroy":
		required, err := requiredCapabilitiesForTopology(s.hclFile(run.Topology))
		if err != nil {
			s.jobs.finish(run, err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.dispatchRun(r.Context(), run, required); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "parent_id": parent.ID, "agent_id": run.AgentID})
}

func (s *Server) reconcileParentJournal(parent, run *Run) error {
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
	if run.Status == RunRunning {
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
	if run.Status == RunRunning {
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

func (s *Server) hclFile(topology string) string {
	return filepath.Join(s.workspacesDir, topology, "field.sysbox.hcl")
}

func (s *Server) stateFile(topology string) string {
	return filepath.Join(s.runsDir, topology, "state.json")
}

func (s *Server) checkpointFile(topology, runID string) string {
	return filepath.Join(s.runsDir, topology, "runs", runID+".checkpoint.json")
}

func (s *Server) loadState(topology string) (*state.State, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return nil, err
	}
	st, err := mgr.Load()
	if err != nil {
		return nil, err
	}
	if s.stateBackend == "" && len(st.Resources) == 0 {
		if _, err := os.Stat(s.stateFile(topology)); err != nil {
			return nil, fmt.Errorf("topology %q: no state file", topology)
		}
	}
	return st, nil
}

func (s *Server) topologyNames(ctx context.Context) ([]string, error) {
	names := map[string]bool{}
	hclEntries, err := filepath.Glob(filepath.Join(s.workspacesDir, "*", "field.sysbox.hcl"))
	if err != nil {
		return nil, err
	}
	for _, e := range hclEntries {
		names[filepath.Base(filepath.Dir(e))] = true
	}
	if s.stateBackend == "" {
		stateEntries, err := filepath.Glob(filepath.Join(s.runsDir, "*", "state.json"))
		if err != nil {
			return nil, err
		}
		for _, e := range stateEntries {
			names[filepath.Base(filepath.Dir(e))] = true
		}
	} else {
		mgr, err := s.stateManager("__list__")
		if err != nil {
			return nil, err
		}
		items, err := mgr.ListTopologies(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			names[item.Name] = true
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		if validatePathSegment(name, "topology") == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func planJSON(p *runtime.Plan) map[string]any {
	add := make([]string, 0, len(p.Add))
	for _, id := range p.Add {
		add = append(add, id.String())
	}
	destroy := make([]string, 0, len(p.Destroy))
	for _, r := range p.Destroy {
		destroy = append(destroy, r.Type+"."+r.Name)
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
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
