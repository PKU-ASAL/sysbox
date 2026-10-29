package runtime

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
)

// OperationRequest contains only resource execution inputs. Dispatch claims,
// Agent sessions, HTTP responses and canonical Run completion stay with callers.
// Inputs are transient; only declared non-sensitive variables are persisted.
type OperationRequest struct {
	ID, Topology, Kind string
	Revision           string
	QueuedAt           time.Time
	Workspace          string
	Target             string
	ResumeID           string
	LockOwner          string
	Inputs             map[string]string
	AllowUnsafeState   bool
}

type OperationDependencies struct {
	Providers *driver.Registry
	Manager   *state.Manager
	Actions   ActionRepository
	Recorder  OperationRecorder
	Log       io.Writer
	Secrets   secret.Resolver
}

// Operation owns workspace loading, planning and state publication. Its caller
// must hold the topology execution lock from preparation through final outcome
// certification, including checks and remote cleanup proof. Repository CAS is
// still required; an in-process lock alone does not authorize provider effects.
type Operation struct {
	Graph       *graph.Graph
	State       *state.State
	Root        *config.Root
	EvalContext *hcl.EvalContext
	request     OperationRequest
	manager     *state.Manager
	executor    *Executor
}

func PrepareOperation(ctx context.Context, req OperationRequest, deps OperationDependencies) (*Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.ID == "" || req.Topology == "" {
		return nil, fmt.Errorf("resource operation identity required")
	}
	if deps.Providers == nil || deps.Manager == nil || deps.Actions == nil {
		return nil, fmt.Errorf("%w: resource operation requires providers, state and action repository", ErrConvergenceUnsupported)
	}
	deps.Manager.AllowUnsafeMutation(req.AllowUnsafeState)
	if err := deps.Manager.CheckMutationSafety(); err != nil {
		return nil, err
	}
	req.Inputs = maps.Clone(req.Inputs)
	op := &Operation{request: req, manager: deps.Manager}
	var err error
	switch req.Kind {
	case "apply", "repair":
		op.Graph, _, op.State, op.Root, op.EvalContext, err = LoadWorkspaceWithInputsContext(ctx, req.Workspace, deps.Manager, req.Inputs, deps.Providers)
	case "reset":
		op.Graph, _, op.State, op.Root, op.EvalContext, err = LoadWorkspaceWithManagerContext(ctx, req.Workspace, deps.Manager, deps.Providers)
	case "destroy":
		op.Graph = graph.New()
		op.State, err = deps.Manager.LoadWithContext(ctx)
	default:
		return nil, fmt.Errorf("unsupported resource operation %q", req.Kind)
	}
	if err != nil {
		return nil, err
	}
	resolver := deps.Secrets
	if resolver == nil {
		resolver = secret.Dispatcher{"env": secret.EnvironmentResolver{}, "input": secret.InputResolver{Inputs: req.Inputs}}
	}
	if req.Kind != "destroy" {
		if err = ValidateArtifactSources(ctx, op.Graph, filepath.Dir(req.Workspace), resolver); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	exec := NewExecutor(op.Graph, op.State, deps.Providers)
	exec.SetRunContext(req.Topology, req.ID)
	exec.SetRunIncarnation(req.QueuedAt, req.Revision)
	exec.SetWorkspaceFromHCLFile(req.Workspace)
	exec.SetOperation(req.Kind)
	exec.SetResumeRunID(req.ResumeID)
	exec.SetActionRepository(deps.Actions)
	exec.SetSecretResolver(resolver)
	if deps.Log == nil {
		deps.Log = io.Discard
	}
	exec.SetLogger(deps.Log)
	if deps.Recorder != nil {
		exec.SetRecorder(deps.Recorder)
	}
	exec.recorder.SetLeaseOwner(req.LockOwner)
	exec.recorder.SetStateSerialBefore(op.State.Meta.Serial)
	op.executor = exec
	return op, nil
}

func (o *Operation) Plan() (*Plan, error) {
	switch o.request.Kind {
	case "apply", "repair":
		plan, err := ComputePlan(o.Graph, o.State, o.executor.providers)
		if err != nil || o.request.Target == "" {
			return plan, err
		}
		target, parseErr := address.Parse(o.request.Target)
		if parseErr != nil || !target.Equal(address.Resource(target.Type, target.Name)) {
			return nil, fmt.Errorf("--target: expected type.name, got %q", o.request.Target)
		}
		o.executor.logf("Targeting: %s\n", target)
		return FilterPlanByTarget(plan, target.Type, target.Name), nil
	case "destroy":
		return BuildDestroyPlan(o.State)
	case "reset":
		return BuildResetPlan(o.Graph, o.State, o.request.Target)
	default:
		return nil, fmt.Errorf("unsupported resource operation %q", o.request.Kind)
	}
}

func BuildDestroyPlan(st *state.State) (*Plan, error) {
	plan := &Plan{}
	for i := len(st.Resources) - 1; i >= 0; i-- {
		r := st.Resources[i]
		if r.LifecyclePreventDestroy() {
			return nil, fmt.Errorf("%s: lifecycle.prevent_destroy blocks deletion", r.Address)
		}
		plan.Actions = append(plan.Actions, model.PlannedChange{Address: r.Address, Action: model.PlanActionDelete, Reason: "destroy requested"})
	}
	return plan, nil
}

// Execute preserves the approved plan. Recovery can remove an already completed
// effect but may not add targets or upgrade a no-op into replacement.
func (o *Operation) Execute(ctx context.Context, plan *Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan == nil {
		return fmt.Errorf("missing plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if snap, err := o.manager.Snapshot(ctx, "before "+o.request.Kind+" "+o.request.ID); err == nil && snap != nil {
		o.executor.logf("State snapshot: %s\n", snap.ID)
	}
	var err error
	switch o.request.Kind {
	case "apply", "repair":
		err = o.executor.Apply(ctx, plan)
	case "destroy":
		err = o.executor.Destroy(ctx, plan)
	case "reset":
		err = o.executor.Reset(ctx, plan)
	}
	if err != nil {
		return err
	}
	if o.request.Kind == "apply" || o.request.Kind == "repair" {
		if err = o.persistVariables(ctx); err != nil {
			return err
		}
	}
	return o.finish(ctx)
}

func (o *Operation) persistVariables(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		o.executor.recorder.Finish(err)
		return err
	}
	latest, err := o.manager.LoadWithContext(ctx)
	if err != nil {
		return err
	}
	if latest.TopologyUID != o.State.TopologyUID || latest.Meta.Serial != o.State.Meta.Serial {
		return fmt.Errorf("applied variables: canonical state changed")
	}
	latest.AppliedVariables = config.NonSensitiveInputValues(o.Root.Variables, o.request.Inputs)
	if err = o.manager.SaveWithLease(ctx, latest, state.LockOptions{Owner: o.request.LockOwner}); err != nil {
		return err
	}
	o.State = latest
	return nil
}
func (o *Operation) finish(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		o.executor.recorder.Finish(err)
		return err
	}
	o.executor.recorder.SetStateSerialAfter(o.State.Meta.Serial)
	o.executor.recorder.MarkResourceStateRecorded()
	if err := o.executor.journalError(); err != nil {
		o.executor.recorder.Finish(err)
		return err
	}
	return nil
}

// The outer authority layer authenticates predecessor identity and stop evidence;
// the resource engine validates the exact reset phases and resumes its own work.
func (o *Operation) PrepareResetContinuation(ctx context.Context, checkpoint *OperationCheckpoint, queued time.Time, revision string) (*ResetContinuationRequest, error) {
	if o.request.Kind != "reset" {
		return nil, fmt.Errorf("operation is not reset")
	}
	return o.executor.PrepareResetContinuation(ctx, checkpoint, queued, revision)
}
func (o *Operation) ContinueReset(ctx context.Context, request ResetContinuationRequest) error {
	if o.request.Kind != "reset" {
		return fmt.Errorf("operation is not reset")
	}
	if err := o.executor.ContinueReset(ctx, request); err != nil {
		return err
	}
	return o.finish(ctx)
}
