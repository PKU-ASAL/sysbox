package api

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
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
	hcl, err := os.ReadFile(hclFile)
	if err != nil {
		return controlplane.Plan{}, err
	}
	fingerprint, err := runtime.BuildPlanFingerprint(planFingerprintInputs(hcl, st, meta.Serial, g))
	if err != nil {
		return controlplane.Plan{}, err
	}
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		return controlplane.Plan{}, err
	}
	plan, err = runtime.NewExecutor(g, st).Refresh(ctx, plan)
	if err != nil {
		return controlplane.Plan{}, err
	}
	var revID string
	if len(hcl) > 0 {
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
		Fingerprint: fingerprint,
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
	if err := plan.CanApply(plan.Revision, plan.StateSerial); err != nil {
		return nil, err
	}
	current.Fingerprint.StateSerial = currentSerial
	if err := runtime.ValidatePlanFingerprint(plan.Fingerprint, current.Fingerprint); err != nil {
		return nil, err
	}
	return plan, nil
}

func planFingerprintInputs(hcl []byte, st *state.State, serial int64, g *graph.Graph) runtime.PlanInputs {
	schemas := map[string]int{}
	for _, node := range g.All() {
		schemas[node.Address.Type] = 1
	}
	drivers := map[string]string{}
	artifacts := map[string]string{}
	for _, resource := range st.Resources {
		if resource.Driver != "" {
			drivers[resource.Address.String()] = resource.Driver
		}
		if digest := resource.Str("sha256"); digest != "" {
			artifacts[resource.Address.String()] = digest
		}
	}
	return runtime.PlanInputs{Config: hcl, StateLineage: st.RunID, StateSerial: serial, ResourceSchemas: schemas, Drivers: drivers, Artifacts: artifacts, Variables: map[string]any{}}
}
