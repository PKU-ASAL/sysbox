package agentexec

import (
	"context"
	"fmt"
	"io"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type Bridge interface {
	AttachRun(run *controlplane.Run)
	LogWriter(runID string) io.Writer
	LockTopology(ctx context.Context, topology string) (func(), error)
	Finish(run *controlplane.Run, err error)
	StateManager(topology string) (*state.Manager, error)
	HCLFile(topology string) string
	CheckpointFile(topology, runID string) string
	CheckpointStore() runtime.CheckpointStore
	ParentRun(ctx context.Context, id string) (*controlplane.Run, error)
	Preflight(ctx context.Context, run *controlplane.Run, log io.Writer) error
	Topologies(ctx context.Context) []string
}

// ActionRepositoryProvider binds runtime journal and resource state to the
// deployment's single atomic backend.
type ActionRepositoryProvider interface {
	ActionRepository(topology string) (runtime.ActionRepository, error)
}

type ApplyHook interface {
	BeforeApply(plan *runtime.Plan) error
}

type DestroyHook interface {
	BeforeDestroy(plan *runtime.Plan) error
}

type ResetHook interface {
	BeforeReset(plan *runtime.Plan) error
}

type ResetContinuationVerifier interface {
	VerifyResetContinuation(context.Context, *controlplane.Run, *controlplane.Run) error
}

type Executor struct {
	bridge Bridge
}

// Remote runs require their dispatched immutable snapshot. Standalone CLI
// bridges may provide an explicitly selected local configuration source.
type runWorkspaceBridge interface {
	HCLFileForRun(*controlplane.Run) string
}

func (e *Executor) hclFileForRun(ctx context.Context, run *controlplane.Run) string {
	if path := RunWorkspace(ctx, run); path != "" {
		return path
	}
	if b, ok := e.bridge.(runWorkspaceBridge); ok {
		if path := b.HCLFileForRun(run); path != "" {
			return path
		}
	}
	return ""
}

func NewExecutorWithBridge(bridge Bridge) *Executor {
	return &Executor{bridge: bridge}
}

// ExecuteContext runs the operation. ctx carries the run's cancellation: it is
// the only path by which a deadline, an operator cancellation or a Ctrl-C
// reaches the provider doing the work, so callers must pass the context they
// were given rather than substituting one.
//
// There is deliberately no context-less variant. The wrapper that used to exist
// substituted context.Background(), which silently severed cancellation for
// every CLI command — the signal context built in main.go was carried all the
// way by cobra and then discarded one call short of here.
func (e *Executor) ExecuteContext(ctx context.Context, run *controlplane.Run) {
	if e.bridge == nil || run == nil {
		return
	}
	e.bridge.AttachRun(run)
	run.Cleanup = e.executeLifecycle(ctx, run, e.bridge.LogWriter(run.ID))
}

func (e *Executor) prepareExecutionContext(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	if prepared, ok := e.bridge.(interface {
		PrepareExecution(context.Context, *controlplane.Run) (context.Context, func(), error)
	}); ok {
		return prepared.PrepareExecution(ctx, run)
	}
	return ctx, func() {}, nil
}

// executeLifecycle owns dispatch preparation and final Run certification only.
// Workspace loading, plans, provider calls and state publication belong to runtime.
func (e *Executor) executeLifecycle(ctx context.Context, run *controlplane.Run, log io.Writer) (proof *controlplane.CleanupReceipt) {
	unlock, err := e.bridge.LockTopology(ctx, run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	defer unlock()
	ctx, release, err := e.prepareExecutionContext(ctx, run)
	if err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	defer release()
	// Publish completion only after the execution function returns normally.
	// A panic must leave its durable intent unresolved, never certify nil error.
	proof, err = e.executeResourceOperation(ctx, run, log)
	e.bridge.Finish(run, err)
	return proof
}

func (e *Executor) executeResourceOperation(ctx context.Context, run *controlplane.Run, log io.Writer) (proof *controlplane.CleanupReceipt, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if run.Op == "reset" && run.ParentID != "" && run.Target != "" {
		err = fmt.Errorf("reset continuation cannot override the original target scope")
		return nil, err
	}
	if run.Op != "destroy" {
		if err = e.bridge.Preflight(ctx, run, log); err != nil {
			return nil, err
		}
	}
	op, recorder, err := e.prepareResourceOperation(ctx, run, log)
	if err != nil {
		return nil, err
	}
	if run.Op == "reset" && run.ParentID != "" {
		hook, ok := e.bridge.(ResetHook)
		if !ok {
			err = fmt.Errorf("reset is not supported by this execution bridge")
			return nil, err
		}
		loader, ok := e.bridge.CheckpointStore().(runtime.CheckpointLoader)
		if !ok {
			err = fmt.Errorf("reset continuation requires durable parent checkpoint")
			return nil, err
		}
		var parent *controlplane.Run
		parent, err = ResolveResetOrigin(ctx, e.bridge, run)
		if err != nil {
			return nil, err
		}
		var checkpoint *runtime.OperationCheckpoint
		checkpoint, err = loader.LoadCheckpoint(ctx, run.Topology, parent.ID)
		if err != nil {
			return nil, err
		}
		var continuation *runtime.ResetContinuationRequest
		continuation, err = op.PrepareResetContinuation(ctx, checkpoint, parent.QueuedAt, parent.Revision)
		if err != nil {
			return nil, err
		}
		if err = hook.BeforeReset(&runtime.Plan{Actions: continuation.Checkpoint.Plan}); err != nil {
			return nil, err
		}
		err = op.ContinueReset(ctx, *continuation)
		return nil, err
	}
	var plan *runtime.Plan
	plan, err = op.Plan()
	if err != nil {
		return nil, err
	}
	switch run.Op {
	case "apply", "repair":
		if hook, ok := e.bridge.(ApplyHook); ok {
			if err = hook.BeforeApply(plan); err != nil {
				return nil, err
			}
		}
		_, _ = log.Write([]byte(plan.Summary() + "\n"))
	case "reset":
		hook, ok := e.bridge.(ResetHook)
		if !ok {
			err = fmt.Errorf("reset is not supported by this execution bridge")
			return nil, err
		}
		if !plan.HasChanges() {
			_, _ = log.Write([]byte("Reset already complete.\n"))
			return nil, err
		}
		if err = hook.BeforeReset(plan); err != nil {
			return nil, err
		}
	case "destroy":
		if hook, ok := e.bridge.(DestroyHook); ok {
			if err = hook.BeforeDestroy(plan); err != nil {
				return nil, err
			}
		}
	}
	if err = op.Execute(ctx, plan); err != nil {
		return nil, err
	}
	switch run.Op {
	case "apply", "repair":
		_, _ = log.Write([]byte("Apply complete.\n"))
		if len(op.Root.Checks) > 0 {
			assertion := evaluateChecks(ctx, op.State, op.Root.Checks, op.EvalContext)
			run.Assertion = &assertion
			for _, failed := range assertion.FailedChecks {
				_, _ = fmt.Fprintf(log, "[check] %s failed: %s\n", failed, assertion.Message)
			}
		}
	case "reset":
		_, _ = log.Write([]byte("Reset complete.\n"))
	case "destroy":
		if run.AgentID != "" && run.Attempt > 0 && run.LeaseOwner != "" {
			proof, err = e.proveCleanup(ctx, *run)
			if err != nil {
				return nil, err
			}
		}
		_, _ = log.Write([]byte("Destroy complete.\n"))
	}
	err = runOutcome(nil, recorder.Err())
	return proof, err
}

func (e *Executor) prepareResourceOperation(ctx context.Context, run *controlplane.Run, log io.Writer) (*runtime.Operation, *runtime.StoreRecorder, error) {
	manager, err := e.bridge.StateManager(run.Topology)
	if err != nil {
		return nil, nil, err
	}
	provider, ok := e.bridge.(ActionRepositoryProvider)
	if !ok {
		return nil, nil, fmt.Errorf("%w: bridge has no atomic action repository", runtime.ErrConvergenceUnsupported)
	}
	actions, err := provider.ActionRepository(run.Topology)
	if err != nil {
		return nil, nil, err
	}
	path := e.bridge.CheckpointFile(run.Topology, run.ID)
	recorder := runtime.NewStoreRecorder(runtime.NewFileRecorder(path, run.ID, run.Topology), e.bridge.CheckpointStore(), run.Topology, run.ID, path).WithContext(ctx)
	request := runtime.OperationRequest{ID: run.ID, Topology: run.Topology, Kind: run.Op, Revision: run.Revision, QueuedAt: run.QueuedAt, Workspace: e.hclFileForRun(ctx, run), Target: run.Target, ResumeID: run.ParentID, LockOwner: run.LeaseOwner, Inputs: run.Inputs, AllowUnsafeState: run.UnsafeState}
	// Local options are input adaptation, never an alternate planner. Explicit
	// Run targets take precedence; continuation retains its authenticated scope.
	if request.Target == "" && !(run.Op == "reset" && run.ParentID != "") {
		if local, ok := e.bridge.(interface{ OperationTarget() string }); ok {
			request.Target = local.OperationTarget()
		}
	}
	op, err := runtime.PrepareOperation(ctx, request, runtime.OperationDependencies{Providers: driver.DefaultRegistry, Manager: manager, Actions: actions, Recorder: recorder, Log: log})
	return op, recorder, err
}

func (e *Executor) proveCleanup(ctx context.Context, run controlplane.Run) (*controlplane.CleanupReceipt, error) {
	provider, ok := e.bridge.(ActionRepositoryProvider)
	if !ok {
		return nil, fmt.Errorf("cleanup proof requires atomic action repository")
	}
	repo, err := provider.ActionRepository(run.Topology)
	if err != nil {
		return nil, err
	}
	prover, ok := repo.(CleanupProver)
	if !ok {
		return nil, fmt.Errorf("cleanup proof is unsupported by action repository")
	}
	return prover.ProveCleanup(ctx, run)
}

// CleanupProver binds canonical cleanup to the remote Run identity.
// This dispatch contract belongs outside the resource engine.
type CleanupProver interface {
	ProveCleanup(context.Context, controlplane.Run) (*controlplane.CleanupReceipt, error)
}
