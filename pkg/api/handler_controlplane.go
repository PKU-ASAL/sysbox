package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func (s *Server) handleListProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": []controlplane.Project{defaultProject()},
	})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if project != controlplane.DefaultProjectID {
		writeError(w, http.StatusNotFound, fmt.Errorf("project not found"))
		return
	}
	writeJSON(w, http.StatusOK, defaultProject())
}

func (s *Server) handleListProjectWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("project") != controlplane.DefaultProjectID {
		writeError(w, http.StatusNotFound, fmt.Errorf("project not found"))
		return
	}
	s.handleListTopologies(w, r)
}

func (s *Server) handleCreateRevision(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	hcl, err := os.ReadFile(s.hclFile(topology))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("workspace HCL not found"))
		return
	}
	rev := revisionFromHCL(topology, hcl, "workspace_hcl")
	if err := s.apiStore.SaveRevision(r.Context(), rev); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, rev)
}

func (s *Server) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	revs, err := s.apiStore.ListRevisions(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(revs) == 0 {
		if hcl, err := os.ReadFile(s.hclFile(topology)); err == nil {
			revs = append(revs, revisionFromHCL(topology, hcl, "workspace_hcl"))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revs})
}

func (s *Server) handleGetRevision(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	revision := r.PathValue("revision")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(revision, "revision"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rev, err := s.apiStore.GetRevision(r.Context(), topology, revision)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := s.computeStoredPlan(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.apiStore.SavePlan(r.Context(), plan); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plans, err := s.apiStore.ListPlans(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (s *Server) handleGetStoredPlan(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	planID := r.PathValue("plan")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(planID, "plan"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := s.apiStore.GetPlan(r.Context(), topology, planID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleGetStackState(w http.ResponseWriter, r *http.Request) {
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
	meta, _ := mgr.Metadata(r.Context())
	st, _ := mgr.Load()
	writeJSON(w, http.StatusOK, controlplane.StackState{
		ProjectID: controlplane.DefaultProjectID,
		Workspace: topology,
		Metadata:  meta,
		State:     st,
	})
}

func (s *Server) handleGetWorkspaceLease(w http.ResponseWriter, r *http.Request) {
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
	lock, err := mgr.LockInfo(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, controlplane.Lease{ProjectID: controlplane.DefaultProjectID, Workspace: topology, Lock: lock})
}

func (s *Server) handleListWorkspaceSnapshots(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	snapshots, err := s.listSnapshots(r.Context(), topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]controlplane.Snapshot, 0, len(snapshots))
	for _, snap := range snapshots {
		out = append(out, controlplane.Snapshot{ProjectID: controlplane.DefaultProjectID, Workspace: topology, Snapshot: snap})
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (s *Server) handleListRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validatePathSegment(id, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.getRunRecord(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	cp, err := s.apiStore.LoadCheckpoint(r.Context(), run.Topology, run.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	events := make([]controlplane.Event, 0, len(cp.Steps))
	for _, step := range cp.Steps {
		events = append(events, controlplane.Event{
			RunID:     run.ID,
			ProjectID: controlplane.DefaultProjectID,
			Workspace: run.Topology,
			Resource:  step.Resource,
			Action:    string(step.Action),
			Status:    string(step.Status),
			Message:   step.Error,
			CreatedAt: step.StartedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) getRunRecord(ctx context.Context, id string) (*Run, error) {
	if run, ok := s.jobs.get(id); ok {
		normalizeRunProductFields(run)
		return run, nil
	}
	return s.apiStore.GetRun(ctx, id)
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, _ *http.Request) {
	root := artifact.DefaultCacheDir()
	items := []controlplane.Artifact{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		items = append(items, controlplane.Artifact{
			ID:        strings.ReplaceAll(rel, string(filepath.Separator), ":"),
			Path:      path,
			Size:      info.Size(),
			CreatedAt: info.ModTime().UTC(),
		})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": items})
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace")
	if workspace != "" {
		if err := validatePathSegment(workspace, "workspace"); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	policies, err := s.apiStore.ListPolicies(r.Context(), workspace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	policy := controlplane.Policy{
		ID:        uuid.NewString(),
		ProjectID: controlplane.DefaultProjectID,
		Mode:      "advisory",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.apiStore.SavePolicy(r.Context(), policy); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, policy)
}

func defaultProject() controlplane.Project {
	now := time.Now().UTC()
	return controlplane.Project{ID: controlplane.DefaultProjectID, Name: "default", Description: "Default sysbox project", CreatedAt: now, UpdatedAt: now}
}

func revisionFromHCL(workspace string, hcl []byte, source string) controlplane.Revision {
	sum := sha256.Sum256(hcl)
	hash := hex.EncodeToString(sum[:])
	id := hash[:12]
	now := time.Now().UTC()
	return controlplane.Revision{
		ID:        id,
		ProjectID: controlplane.DefaultProjectID,
		Workspace: workspace,
		Source:    source,
		SHA256:    hash,
		Size:      len(hcl),
		CreatedAt: now,
	}
}

func (s *Server) computeStoredPlan(ctx context.Context, topology string) (controlplane.Plan, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return controlplane.Plan{}, err
	}
	g, _, st, _, _, err := runtime.LoadWorkspaceWithManager(s.hclFile(topology), mgr)
	if err != nil {
		return controlplane.Plan{}, err
	}
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		return controlplane.Plan{}, err
	}
	runtime.NewExecutor(g, st).Refresh(ctx, plan)
	var revID string
	if hcl, err := os.ReadFile(s.hclFile(topology)); err == nil {
		rev := revisionFromHCL(topology, hcl, "workspace_hcl")
		revID = rev.ID
		_ = s.apiStore.SaveRevision(ctx, rev)
	}
	return controlplane.Plan{
		ID:        uuid.NewString(),
		ProjectID: controlplane.DefaultProjectID,
		Workspace: topology,
		Revision:  revID,
		Status:    "planned",
		Summary:   plan.Summary(),
		Actions:   plan.Actions,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Server) validateStoredPlanForApply(ctx context.Context, topology, planID string) (*controlplane.Plan, error) {
	plan, err := s.apiStore.GetPlan(ctx, topology, planID)
	if err != nil {
		return nil, err
	}
	current, err := s.computeStoredPlan(ctx, topology)
	if err != nil {
		return nil, err
	}
	if plan.Revision != "" && current.Revision != "" && plan.Revision != current.Revision {
		return nil, fmt.Errorf("plan revision %s is stale; current revision is %s", plan.Revision, current.Revision)
	}
	if plan.Status != "" && plan.Status != "planned" {
		return nil, fmt.Errorf("plan %s status is %s", plan.ID, plan.Status)
	}
	return plan, nil
}

func (s *Server) listSnapshots(ctx context.Context, topology string) ([]state.Snapshot, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return nil, err
	}
	snapshots, ok := mgr.Backend().(state.SnapshotBackend)
	if !ok {
		return nil, nil
	}
	return snapshots.ListSnapshots(ctx)
}
