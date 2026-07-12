package api

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type PlanService struct {
	plans        planStore
	revs         revisionStore
	hclFile      func(string) string
	stateManager func(string) (*state.Manager, error)
}

func newPlanService(server *Server) *PlanService {
	return &PlanService{
		plans:        server.apiStore,
		revs:         server.apiStore,
		hclFile:      server.workspaceService().HCLFile,
		stateManager: server.stateManager,
	}
}

func (s *PlanService) ComputeStoredPlan(ctx context.Context, topology string) (controlplane.Plan, error) {
	hclFile := s.hclFile(topology)
	mgr, err := s.stateManager(topology)
	if err != nil {
		return controlplane.Plan{}, err
	}
	g, _, st, _, _, err := runtime.LoadWorkspaceWithManager(hclFile, mgr)
	if err != nil {
		return controlplane.Plan{}, err
	}
	meta, _ := mgr.Metadata(ctx)
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		return controlplane.Plan{}, err
	}
	plan, err = runtime.NewExecutor(g, st).Refresh(ctx, plan)
	if err != nil {
		return controlplane.Plan{}, err
	}
	var revID string
	if hcl, err := os.ReadFile(hclFile); err == nil {
		rev := revisionFromHCL(topology, hcl, "workspace_hcl")
		revID = rev.ID
		_ = s.revs.SaveRevision(ctx, rev)
	}
	return controlplane.Plan{
		ID:          uuid.NewString(),
		ProjectID:   controlplane.DefaultProjectID,
		Workspace:   topology,
		Revision:    revID,
		StateSerial: meta.Serial,
		Status:      controlplane.PlanStatusPlanned,
		Summary:     plan.Summary(),
		Actions:     plan.Actions,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (s *PlanService) ValidateStoredPlanForApply(ctx context.Context, topology, planID string, currentSerial int64) (*controlplane.Plan, error) {
	plan, err := s.plans.GetPlan(ctx, topology, planID)
	if err != nil {
		return nil, err
	}
	current, err := s.ComputeStoredPlan(ctx, topology)
	if err != nil {
		return nil, err
	}
	if err := plan.CanApply(current.Revision, currentSerial); err != nil {
		return nil, err
	}
	return plan, nil
}
