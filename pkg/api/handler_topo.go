package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/oslab/sysbox/pkg/graph"
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
		topolist[name] = &topologyInfo{Name: name, HasHCL: true}
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
				info = &topologyInfo{Name: name}
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
				info = &topologyInfo{Name: item.Name}
				topolist[item.Name] = info
			}
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
	run := s.jobs.start(topology, "apply")
	go s.runApply(topology, run)
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})
}

func (s *Server) runApply(topology string, run *Run) {
	unlock := s.jobs.lockTopology(topology)
	defer unlock()

	preflight, err := s.preflightTopology(topology)
	if err != nil {
		s.jobs.finish(run, err)
		return
	}
	writePreflightLogs(run, preflight)
	if err := preflight.err(); err != nil {
		s.jobs.finish(run, err)
		return
	}

	mgr, err := s.stateManager(topology)
	if err != nil {
		s.jobs.finish(run, err)
		return
	}
	g, mgr, st, _, _, err := runtime.LoadWorkspaceWithManager(s.hclFile(topology), mgr)
	if err != nil {
		s.jobs.finish(run, err)
		return
	}
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		s.jobs.finish(run, err)
		return
	}
	exec := runtime.NewExecutor(g, st)
	exec.SetRunContext(topology, run.ID)
	exec.SetLogger(run.logs)
	recorder := runtime.NewFileRecorder(s.checkpointFile(topology, run.ID), run.ID, topology)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	exec.Refresh(context.Background(), plan)
	if !plan.HasChanges() {
		_, _ = run.logs.Write([]byte("No changes. Apply is a no-op.\n"))
		s.jobs.finish(run, nil)
		return
	}
	_, _ = run.logs.Write([]byte(plan.Summary() + "\n"))
	if snap, err := mgr.Snapshot(context.Background(), "before apply "+run.ID); err == nil && snap != nil {
		_, _ = run.logs.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Apply(context.Background(), plan); err != nil {
		if saveErr := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); saveErr != nil {
			_, _ = run.logs.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
		} else {
			recorder.SetStateSerialAfter(st.Meta.Serial)
		}
		s.jobs.finish(run, err)
		return
	}
	saveStep := recorder.StepStartKind("state", "state", runtime.PlanActionUpdate)
	if err := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
		recorder.StepFailed(saveStep, err)
		recorder.Finish(err)
		s.jobs.finish(run, fmt.Errorf("save state: %w", err))
		return
	}
	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.StepDone(saveStep)
	recorder.MarkResourceStateRecorded()
	_, _ = run.logs.Write([]byte("Apply complete.\n"))
	s.jobs.finish(run, nil)
}

func writePreflightLogs(run *Run, res *preflightResult) {
	if res == nil {
		return
	}
	_, _ = run.logs.Write([]byte("Preflight checks:\n"))
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
		_, _ = run.logs.Write([]byte(line))
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
	go s.runDestroy(topology, run)
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})
}

func (s *Server) runDestroy(topology string, run *Run) {
	unlock := s.jobs.lockTopology(topology)
	defer unlock()

	mgr, err := s.stateManager(topology)
	if err != nil {
		s.jobs.finish(run, err)
		return
	}
	st, err := mgr.Load()
	if err != nil {
		s.jobs.finish(run, err)
		return
	}
	if len(st.Resources) == 0 {
		_, _ = run.logs.Write([]byte("Nothing to destroy.\n"))
		s.jobs.finish(run, nil)
		return
	}
	plan := &runtime.Plan{Destroy: append([]state.Resource(nil), st.Resources...)}
	exec := runtime.NewExecutor(graph.New(), st)
	exec.SetRunContext(topology, run.ID)
	exec.SetLogger(run.logs)
	recorder := runtime.NewFileRecorder(s.checkpointFile(topology, run.ID), run.ID, topology)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	if snap, err := mgr.Snapshot(context.Background(), "before destroy "+run.ID); err == nil && snap != nil {
		_, _ = run.logs.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Destroy(context.Background(), plan); err != nil {
		if saveErr := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); saveErr != nil {
			_, _ = run.logs.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
		} else {
			recorder.SetStateSerialAfter(st.Meta.Serial)
		}
		s.jobs.finish(run, err)
		return
	}
	saveStep := recorder.StepStartKind("state", "state", runtime.PlanActionUpdate)
	if err := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
		recorder.StepFailed(saveStep, err)
		recorder.Finish(err)
		s.jobs.finish(run, fmt.Errorf("save state: %w", err))
		return
	}
	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.StepDone(saveStep)
	recorder.MarkResourceStateRecorded()
	_, _ = run.logs.Write([]byte("Destroy complete.\n"))
	s.jobs.finish(run, nil)
}

// GET /v1/runs/{id}
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
		go s.runApply(run.Topology, run)
	case "destroy":
		go s.runDestroy(run.Topology, run)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID, "parent_id": parent.ID})
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
	report, err := cleanupCheckpoint(r.Context(), s.checkpointFile(run.Topology, run.ID))
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
	report, err := recoverCheckpoint(r.Context(), s.checkpointFile(run.Topology, run.ID), mgr, owner)
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
	data, err := os.ReadFile(s.checkpointFile(run.Topology, run.ID))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
	ch := run.logs.Subscribe()
	defer run.logs.Unsubscribe(ch)
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
