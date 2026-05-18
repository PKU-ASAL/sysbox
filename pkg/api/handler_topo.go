package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// GET /v1/health
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /v1/topologies
// Scans runs/*/state.json and returns the list of known suites.
func (s *Server) handleListTopologies(w http.ResponseWriter, r *http.Request) {
	entries, err := filepath.Glob(filepath.Join(s.runsDir, "*", "state.json"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	suites := make([]string, 0, len(entries))
	for _, e := range entries {
		suites = append(suites, filepath.Base(filepath.Dir(e)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"suites": suites})
}

// GET /v1/topologies/{suite}/state
func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	st, err := s.loadState(r.PathValue("suite"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// GET /v1/topologies/{suite}/plan
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	suite := r.PathValue("suite")
	g, _, st, _, _, err := runtime.LoadWorkspace(s.hclFile(suite), s.stateFile(suite))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, planJSON(plan))
}

// POST /v1/topologies/{suite}/apply
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	suite := r.PathValue("suite")
	run := s.jobs.start(suite, "apply")

	go func() {
		g, mgr, st, _, _, err := runtime.LoadWorkspace(s.hclFile(suite), s.stateFile(suite))
		if err != nil {
			s.jobs.finish(run, err)
			return
		}
		plan, err := runtime.ComputePlan(g, st)
		if err != nil {
			s.jobs.finish(run, err)
			return
		}
		if !plan.HasChanges() {
			run.logs.Write([]byte("No changes. Apply is a no-op.\n")) //nolint:errcheck
			s.jobs.finish(run, nil)
			return
		}
		run.logs.Write([]byte(plan.Summary() + "\n")) //nolint:errcheck
		exec := runtime.NewExecutor(g, st)
		exec.SetLogger(run.logs)
		if err := exec.Apply(context.Background(), plan); err != nil {
			_ = mgr.Save(st)
			s.jobs.finish(run, err)
			return
		}
		_ = mgr.Save(st)
		run.logs.Write([]byte("Apply complete.\n")) //nolint:errcheck
		s.jobs.finish(run, nil)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})
}

// POST /v1/topologies/{suite}/destroy
func (s *Server) handleDestroy(w http.ResponseWriter, r *http.Request) {
	suite := r.PathValue("suite")
	run := s.jobs.start(suite, "destroy")

	go func() {
		stateFile := s.stateFile(suite)
		mgr := state.NewManager(stateFile)
		st, err := mgr.Load()
		if err != nil {
			s.jobs.finish(run, err)
			return
		}
		if len(st.Resources) == 0 {
			run.logs.Write([]byte("Nothing to destroy.\n")) //nolint:errcheck
			s.jobs.finish(run, nil)
			return
		}
		plan := &runtime.Plan{Destroy: append([]state.Resource(nil), st.Resources...)}
		exec := runtime.NewExecutor(graph.New(), st)
		exec.SetLogger(run.logs)
		if err := exec.Destroy(context.Background(), plan); err != nil {
			_ = mgr.Save(st)
			s.jobs.finish(run, err)
			return
		}
		_ = mgr.Save(st)
		run.logs.Write([]byte("Destroy complete.\n")) //nolint:errcheck
		s.jobs.finish(run, nil)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})
}

// GET /v1/runs/{id}
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// GET /v1/runs/{id}/logs  — SSE stream
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	run, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := run.logs.Subscribe()
	defer run.logs.Unsubscribe(ch)
	ServeSSE(w, ch)
}

// helpers

func (s *Server) hclFile(suite string) string {
	return filepath.Join("examples", suite, "field.sysbox.hcl")
}

func (s *Server) stateFile(suite string) string {
	return filepath.Join(s.runsDir, suite, "state.json")
}

func (s *Server) loadState(suite string) (*state.State, error) {
	f := s.stateFile(suite)
	if _, err := os.Stat(f); err != nil {
		return nil, fmt.Errorf("suite %q: no state file", suite)
	}
	return state.NewManager(f).Load()
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
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
