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
	topolist := map[string]map[string]bool{}

	hclEntries, err := filepath.Glob(filepath.Join(s.workspacesDir, "*", "field.sysbox.hcl"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, e := range hclEntries {
		name := filepath.Base(filepath.Dir(e))
		topolist[name] = map[string]bool{"hcl": true, "state": false}
	}

	stateEntries, err := filepath.Glob(filepath.Join(s.runsDir, "*", "state.json"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, e := range stateEntries {
		name := filepath.Base(filepath.Dir(e))
		if topolist[name] == nil {
			topolist[name] = map[string]bool{"hcl": false, "state": true}
		} else {
			topolist[name]["state"] = true
		}
	}

	out := make([]map[string]any, 0, len(topolist))
	for name, flags := range topolist {
		out = append(out, map[string]any{
			"name":       name,
			"has_hcl":    flags["hcl"],
			"has_state":  flags["state"],
		})
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

// GET /v1/topologies/{topology}/plan
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, _, st, _, _, err := runtime.LoadWorkspace(s.hclFile(topology), s.stateFile(topology))
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

// POST /v1/topologies/{topology}/apply
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run := s.jobs.start(topology, "apply")

	go func() {
		unlock := s.jobs.lockTopology(topology)
		defer unlock()

		g, mgr, st, _, _, err := runtime.LoadWorkspace(s.hclFile(topology), s.stateFile(topology))
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
			_, _ = run.logs.Write([]byte("No changes. Apply is a no-op.\n"))
			s.jobs.finish(run, nil)
			return
		}
		_, _ = run.logs.Write([]byte(plan.Summary() + "\n"))
		exec := runtime.NewExecutor(g, st)
		exec.SetLogger(run.logs)
		if err := exec.Apply(context.Background(), plan); err != nil {
			if saveErr := mgr.Save(st); saveErr != nil {
				_, _ = run.logs.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
			}
			s.jobs.finish(run, err)
			return
		}
		if err := mgr.Save(st); err != nil {
			s.jobs.finish(run, fmt.Errorf("save state: %w", err))
			return
		}
		_, _ = run.logs.Write([]byte("Apply complete.\n"))
		s.jobs.finish(run, nil)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})
}

// POST /v1/topologies/{topology}/destroy
func (s *Server) handleDestroy(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run := s.jobs.start(topology, "destroy")

	go func() {
		unlock := s.jobs.lockTopology(topology)
		defer unlock()

		stateFile := s.stateFile(topology)
		mgr := state.NewManager(stateFile)
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
		exec.SetLogger(run.logs)
		if err := exec.Destroy(context.Background(), plan); err != nil {
			if saveErr := mgr.Save(st); saveErr != nil {
				_, _ = run.logs.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
			}
			s.jobs.finish(run, err)
			return
		}
		if err := mgr.Save(st); err != nil {
			s.jobs.finish(run, fmt.Errorf("save state: %w", err))
			return
		}
		_, _ = run.logs.Write([]byte("Destroy complete.\n"))
		s.jobs.finish(run, nil)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})
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

func (s *Server) loadState(topology string) (*state.State, error) {
	f := s.stateFile(topology)
	if _, err := os.Stat(f); err != nil {
		return nil, fmt.Errorf("topology %q: no state file", topology)
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
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("writeJSON encode failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
