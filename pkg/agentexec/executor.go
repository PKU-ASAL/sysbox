package agentexec

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/secret"
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
	ReconcileParentJournal(parent, run *controlplane.Run) error
	Preflight(ctx context.Context, run *controlplane.Run, log io.Writer) error
	OpenConsole(ctx context.Context, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest, ws *websocket.Conn) error
	Topologies(ctx context.Context) []string
}

type Reporter interface {
	ReportRunComplete(ctx context.Context, run *controlplane.Run, projection controlplane.Projection) error
}

// ActionRepositoryProvider binds runtime journal and resource state to the
// deployment's single atomic backend.
type ActionRepositoryProvider interface {
	ActionRepository(topology string) (runtime.ActionRepository, error)
}

func (e *Executor) installActionRepository(exec *runtime.Executor, topology string) error {
	if provider, ok := e.bridge.(ActionRepositoryProvider); ok {
		repository, err := provider.ActionRepository(topology)
		if err != nil {
			return err
		}
		if repository == nil {
			return fmt.Errorf("action repository unavailable")
		}
		exec.SetActionRepository(repository)
		return nil
	}
	if repository, ok := e.bridge.CheckpointStore().(runtime.ActionRepository); ok {
		exec.SetActionRepository(repository)
		return nil
	}
	return fmt.Errorf("%w: bridge has no atomic action repository", runtime.ErrConvergenceUnsupported)
}

type ApplyHook interface {
	FilterApplyPlan(plan *runtime.Plan) (*runtime.Plan, error)
	RefreshApply() bool
	BeforeApply(plan *runtime.Plan) error
}

type DestroyHook interface {
	BuildDestroyPlan(st *state.State) (*runtime.Plan, error)
	BeforeDestroy(plan *runtime.Plan) error
}

type ResetHook interface {
	BuildResetPlan(g *graph.Graph, st *state.State, target string) (*runtime.Plan, error)
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
	if _, remote := e.bridge.(remoteBridge); remote {
		return ""
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
	log := e.bridge.LogWriter(run.ID)
	var cleanup *controlplane.CleanupReceipt
	switch run.Op {
	case "apply", "repair":
		e.executeApply(ctx, run, log)
	case "destroy":
		cleanup = e.executeDestroy(ctx, run, log)
	case "reset":
		e.executeReset(ctx, run, log)
	default:
		e.bridge.Finish(run, fmt.Errorf("unsupported run op %q", run.Op))
	}
	run.Cleanup = cleanup
	report := *run
	e.reportCompletion(ctx, &report)
}

func (e *Executor) prepareExecutionContext(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	if prepared, ok := e.bridge.(interface {
		PrepareExecution(context.Context, *controlplane.Run) (context.Context, func(), error)
	}); ok {
		return prepared.PrepareExecution(ctx, run)
	}
	return ctx, func() {}, nil
}

func (e *Executor) executeReset(ctx context.Context, run *controlplane.Run, log io.Writer) {
	if run.ParentID != "" && run.Target != "" {
		e.bridge.Finish(run, fmt.Errorf("reset continuation cannot override the original target scope"))
		return
	}
	unlock, err := e.bridge.LockTopology(ctx, run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	defer unlock()
	ctx, release, err := e.prepareExecutionContext(ctx, run)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	defer release()

	if err := ctx.Err(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := e.bridge.Preflight(ctx, run, log); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	mgr, err := e.bridge.StateManager(run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	mgr.AllowUnsafeMutation(run.UnsafeState)
	if err := mgr.CheckMutationSafety(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	g, mgr, st, _, _, err := runtime.LoadWorkspaceWithManager(e.hclFileForRun(ctx, run), mgr)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := runtime.ValidateArtifactSources(ctx, g, filepath.Dir(e.hclFileForRun(ctx, run)), secret.EnvironmentResolver{}); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	hook, ok := e.bridge.(ResetHook)
	if !ok {
		e.bridge.Finish(run, fmt.Errorf("reset is not supported by this execution bridge"))
		return
	}
	var plan *runtime.Plan
	if run.ParentID == "" {
		plan, err = hook.BuildResetPlan(g, st, run.Target)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		if !plan.HasChanges() {
			_, _ = log.Write([]byte("Reset already complete.\n"))
			e.bridge.Finish(run, nil)
			return
		}
		if err := hook.BeforeReset(plan); err != nil {
			e.bridge.Finish(run, err)
			return
		}
	}

	exec := runtime.NewExecutor(g, st)
	exec.SetRunContext(run.Topology, run.ID)
	exec.SetRunIncarnation(run.QueuedAt, run.Revision)
	exec.SetWorkspaceFromHCLFile(e.hclFileForRun(ctx, run))
	exec.SetOperation(run.Op)
	exec.SetResumeRunID(run.ParentID)
	exec.SetLogger(log)
	exec.SetSecretResolver(secret.Dispatcher{
		"env":   secret.EnvironmentResolver{},
		"input": secret.InputResolver{Inputs: run.Inputs},
	})
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath).WithContext(ctx)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	if err := e.installActionRepository(exec, run.Topology); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if run.ParentID != "" {
		loader, ok := e.bridge.CheckpointStore().(runtime.CheckpointLoader)
		if !ok {
			e.bridge.Finish(run, fmt.Errorf("reset continuation requires durable parent checkpoint"))
			return
		}
		parent, err := ResolveResetOrigin(ctx, e.bridge, run)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		checkpoint, err := loader.LoadCheckpoint(ctx, run.Topology, parent.ID)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		request, err := exec.PrepareResetContinuation(ctx, checkpoint, parent.QueuedAt, parent.Revision)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		if err := hook.BeforeReset(&runtime.Plan{Actions: request.Checkpoint.Plan}); err != nil {
			e.bridge.Finish(run, err)
			return
		}
		if err := exec.ContinueReset(ctx, *request); err != nil {
			e.bridge.Finish(run, err)
			return
		}
		recorder.SetStateSerialAfter(st.Meta.Serial)
		recorder.MarkResourceStateRecorded()
		if err := recorder.Err(); err != nil {
			recorder.Finish(err)
			e.bridge.Finish(run, err)
			return
		}
		e.bridge.Finish(run, nil)
		return
	}
	if snap, err := mgr.Snapshot(ctx, "before reset "+run.ID); err == nil && snap != nil {
		_, _ = log.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Reset(ctx, plan); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.MarkResourceStateRecorded()
	if err := recorder.Err(); err != nil {
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return
	}
	_, _ = log.Write([]byte("Reset complete.\n"))
	e.bridge.Finish(run, runOutcome(nil, recorder.Err()))
}

func (e *Executor) reportCompletion(ctx context.Context, run *controlplane.Run) {
	reporter, ok := e.bridge.(Reporter)
	if !ok || run == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	proj := controlplane.Projection{
		AgentID:   run.AgentID,
		Workspace: run.Workspace,
		Topology:  run.Topology,
		UpdatedAt: run.EndedAt,
	}
	if mgr, err := e.bridge.StateManager(run.Topology); err == nil {
		if meta, err := mgr.Metadata(ctx); err == nil {
			proj.Backend = meta.Backend
			proj.Serial = meta.Serial
			proj.UpdatedAt = meta.UpdatedAt
		}
		if st, err := mgr.LoadWithContext(ctx); err == nil && st != nil {
			proj.ResourceCount = len(st.Resources)
		}
	}
	if proj.Health == "" {
		if run.Status == controlplane.RunDone {
			proj.Health = "healthy"
		} else {
			proj.Health = "unknown"
		}
	}
	// Report synchronously so the common path completes immediately. On a
	// transient failure, keep retrying in the background: the endpoint is
	// idempotent and the run's lease (30m) remains the final fallback if the
	// agent itself crashes before this succeeds.
	if err := reporter.ReportRunComplete(ctx, run, proj); err != nil {
		fmt.Printf("[agent] report run complete %s failed, retrying in background: %v\n", run.ID, err)
		go func() {
			retryCtx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()
			if err := reportRunCompleteWithRetry(retryCtx, reporter, run, proj); err != nil {
				fmt.Printf("[agent] report run complete %s failed after background retries: %v\n", run.ID, err)
			}
		}()
	}
}

var reportRunCompleteRetryInterval = 5 * time.Second

func reportRunCompleteWithRetry(ctx context.Context, reporter Reporter, run *controlplane.Run, proj controlplane.Projection) error {
	ticker := time.NewTicker(reportRunCompleteRetryInterval)
	defer ticker.Stop()
	for {
		if err := reporter.ReportRunComplete(ctx, run, proj); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *Executor) executeApply(ctx context.Context, run *controlplane.Run, log io.Writer) {
	unlock, err := e.bridge.LockTopology(ctx, run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	defer unlock()
	ctx, release, err := e.prepareExecutionContext(ctx, run)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	defer release()

	if err := ctx.Err(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := e.bridge.Preflight(ctx, run, log); err != nil {
		e.bridge.Finish(run, err)
		return
	}

	mgr, err := e.bridge.StateManager(run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	mgr.AllowUnsafeMutation(run.UnsafeState)
	if err := mgr.CheckMutationSafety(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	g, mgr, st, root, evalCtx, err := runtime.LoadWorkspaceWithInputs(e.hclFileForRun(ctx, run), mgr, run.Inputs)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := runtime.ValidateArtifactSources(ctx, g, filepath.Dir(e.hclFileForRun(ctx, run)), secret.Dispatcher{"env": secret.EnvironmentResolver{}, "input": secret.InputResolver{Inputs: run.Inputs}}); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	plan, err := runtime.ComputePlan(g, st)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if hook, ok := e.bridge.(ApplyHook); ok {
		plan, err = hook.FilterApplyPlan(plan)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
	}
	exec := runtime.NewExecutor(g, st)
	exec.SetRunContext(run.Topology, run.ID)
	exec.SetRunIncarnation(run.QueuedAt, run.Revision)
	exec.SetWorkspaceFromHCLFile(e.hclFileForRun(ctx, run))
	exec.SetOperation(run.Op)
	exec.SetLogger(log)
	exec.SetSecretResolver(secret.Dispatcher{
		"env":   secret.EnvironmentResolver{},
		"input": secret.InputResolver{Inputs: run.Inputs},
	})
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath).WithContext(ctx)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	if err := e.installActionRepository(exec, run.Topology); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := ctx.Err(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if hook, ok := e.bridge.(ApplyHook); ok {
		if err := hook.BeforeApply(plan); err != nil {
			e.bridge.Finish(run, err)
			return
		}
	}
	_, _ = log.Write([]byte(plan.Summary() + "\n"))
	if snap, err := mgr.Snapshot(ctx, "before apply "+run.ID); err == nil && snap != nil {
		_, _ = log.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Apply(ctx, plan); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := ctx.Err(); err != nil {
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return
	}
	// Reload canonical state after journal commits: the executor's working copy
	// must never overwrite resources committed by the action repository.
	latest, err := mgr.LoadWithContext(ctx)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if latest.TopologyUID != st.TopologyUID || latest.Meta.Serial != st.Meta.Serial {
		e.bridge.Finish(run, fmt.Errorf("applied variables: canonical state changed"))
		return
	}
	latest.AppliedVariables = config.NonSensitiveInputValues(root.Variables, run.Inputs)
	if err := mgr.SaveWithLease(ctx, latest, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	st = latest

	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.MarkResourceStateRecorded()
	if err := recorder.Err(); err != nil {
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return
	}
	_, _ = log.Write([]byte("Apply complete.\n"))

	// Checks run after apply: their probes (a command inside a node, a
	// reachability probe) can only run against the built topology. The outcome
	// is stored on the run for the readiness projection; a check failure does
	// not fail the apply — it is reported, not enforced.
	if len(root.Checks) > 0 {
		assertion := evaluateChecks(ctx, st, root.Checks, evalCtx)
		run.Assertion = &assertion
		for _, failed := range assertion.FailedChecks {
			_, _ = log.Write([]byte(fmt.Sprintf("[check] %s failed: %s\n", failed, assertion.Message)))
		}
	}

	e.bridge.Finish(run, runOutcome(nil, recorder.Err()))
}

func (e *Executor) executeDestroy(ctx context.Context, run *controlplane.Run, log io.Writer) *controlplane.CleanupReceipt {
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

	if err := ctx.Err(); err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	mgr, err := e.bridge.StateManager(run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	mgr.AllowUnsafeMutation(run.UnsafeState)
	if err := mgr.CheckMutationSafety(); err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	st, err := mgr.Load()
	if err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	plan := defaultDestroyPlan(st)
	if hook, ok := e.bridge.(DestroyHook); ok {
		plan, err = hook.BuildDestroyPlan(st)
		if err != nil {
			e.bridge.Finish(run, err)
			return nil
		}
		if err := hook.BeforeDestroy(plan); err != nil {
			e.bridge.Finish(run, err)
			return nil
		}
	}
	exec := runtime.NewExecutor(graph.New(), st)
	exec.SetRunContext(run.Topology, run.ID)
	exec.SetRunIncarnation(run.QueuedAt, run.Revision)
	exec.SetWorkspaceFromHCLFile(e.hclFileForRun(ctx, run))
	exec.SetLogger(log)
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath).WithContext(ctx)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	if snap, err := mgr.Snapshot(ctx, "before destroy "+run.ID); err == nil && snap != nil {
		_, _ = log.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := e.installActionRepository(exec, run.Topology); err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	if err := exec.Destroy(ctx, plan); err != nil {
		e.bridge.Finish(run, err)
		return nil
	}
	if err := ctx.Err(); err != nil {
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return nil
	}
	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.MarkResourceStateRecorded()
	if err := recorder.Err(); err != nil {
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return nil
	}
	var proof *controlplane.CleanupReceipt
	if run.AgentID != "" && run.Attempt > 0 && run.LeaseOwner != "" {
		proof, err = e.proveCleanup(ctx, *run)
		if err != nil {
			e.bridge.Finish(run, err)
			return nil
		}
	}
	_, _ = log.Write([]byte("Destroy complete.\n"))
	e.bridge.Finish(run, runOutcome(nil, recorder.Err()))
	return proof
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
	prover, ok := repo.(runtime.CleanupProver)
	if !ok {
		return nil, fmt.Errorf("cleanup proof is unsupported by action repository")
	}
	return prover.ProveCleanup(ctx, run)
}

func defaultDestroyPlan(st *state.State) *runtime.Plan {
	plan := &runtime.Plan{}
	for i := len(st.Resources) - 1; i >= 0; i-- {
		r := st.Resources[i]
		if r.LifecyclePreventDestroy() {
			return &runtime.Plan{Actions: []controlplane.PlannedChange{{Address: r.Address, Action: controlplane.PlanActionUnknown, Reason: "lifecycle.prevent_destroy blocks deletion"}}}
		}
		plan.Actions = append(plan.Actions, controlplane.PlannedChange{
			Address: r.Address, Action: controlplane.PlanActionDelete, Reason: "destroy requested",
		})
	}
	return plan
}
