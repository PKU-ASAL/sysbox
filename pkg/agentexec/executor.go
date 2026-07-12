package agentexec

import (
	"context"
	"fmt"
	"io"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type Bridge interface {
	AttachRun(run *controlplane.Run)
	LogWriter(runID string) io.Writer
	LockTopology(topology string) func()
	Finish(run *controlplane.Run, err error)
	StateManager(topology string) (*state.Manager, error)
	HCLFile(topology string) string
	CheckpointFile(topology, runID string) string
	CheckpointStore() runtime.CheckpointStore
	ValidateStoredPlanForApply(ctx context.Context, topology, planID string, currentSerial int64) (*controlplane.Plan, error)
	ParentRun(ctx context.Context, id string) (*controlplane.Run, error)
	ReconcileParentJournal(parent, run *controlplane.Run) error
	Preflight(ctx context.Context, topology string, log io.Writer) error
	OpenConsole(ctx context.Context, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest, ws *websocket.Conn) error
	Topologies(ctx context.Context) []string
}

type Reporter interface {
	ReportRunComplete(ctx context.Context, run *controlplane.Run, projection controlplane.Projection) error
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

type Executor struct {
	bridge Bridge
}

func NewExecutorWithBridge(bridge Bridge) *Executor {
	return &Executor{bridge: bridge}
}

func (e *Executor) Execute(run *controlplane.Run) {
	e.ExecuteContext(context.Background(), run)
}

func (e *Executor) ExecuteContext(ctx context.Context, run *controlplane.Run) {
	if e.bridge == nil || run == nil {
		return
	}
	e.bridge.AttachRun(run)
	log := e.bridge.LogWriter(run.ID)
	switch run.Op {
	case "apply", "repair":
		if run.ParentID != "" {
			if parent, err := e.bridge.ParentRun(ctx, run.ParentID); err == nil {
				e.executeResumeApply(ctx, parent, run, log)
				break
			}
		}
		e.executeApply(ctx, run, log)
	case "destroy":
		if run.ParentID != "" {
			if parent, err := e.bridge.ParentRun(ctx, run.ParentID); err == nil {
				e.executeResumeDestroy(ctx, parent, run, log)
				break
			}
		}
		e.executeDestroy(ctx, run, log)
	default:
		e.bridge.Finish(run, fmt.Errorf("unsupported run op %q", run.Op))
	}
	e.reportCompletion(ctx, run)
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
	_ = reporter.ReportRunComplete(ctx, run, proj)
}

func (e *Executor) executeApply(ctx context.Context, run *controlplane.Run, log io.Writer) {
	unlock := e.bridge.LockTopology(run.Topology)
	defer unlock()

	if err := ctx.Err(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if err := e.bridge.Preflight(ctx, run.Topology, log); err != nil {
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
	g, mgr, st, _, _, err := runtime.LoadWorkspaceWithManager(e.bridge.HCLFile(run.Topology), mgr)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	meta, _ := mgr.Metadata(ctx)
	var plan *runtime.Plan
	if run.PlanID != "" {
		stored, err := e.bridge.ValidateStoredPlanForApply(ctx, run.Topology, run.PlanID, meta.Serial)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		plan = &runtime.Plan{Actions: append([]controlplane.PlannedChange(nil), stored.Actions...)}
	} else {
		plan, err = runtime.ComputePlan(g, st)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
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
	exec.SetOperation(run.Op)
	exec.SetLogger(log)
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath).WithContext(ctx)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	exec.SetStatePatchSink(&runtime.StatePatchManagerSink{Manager: mgr, State: st, Owner: run.LeaseOwner})
	refresh := run.PlanID == ""
	if hook, ok := e.bridge.(ApplyHook); ok {
		refresh = hook.RefreshApply()
	}
	if refresh {
		plan, err = exec.Refresh(ctx, plan)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
	}
	if err := ctx.Err(); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if !plan.HasChanges() {
		_, _ = log.Write([]byte("No changes. Apply is a no-op.\n"))
		e.bridge.Finish(run, nil)
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
		if saveErr := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: run.LeaseOwner}); saveErr != nil {
			_, _ = log.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
		} else {
			recorder.SetStateSerialAfter(st.Meta.Serial)
		}
		e.bridge.Finish(run, err)
		return
	}
	saveStep := recorder.StepStartKind("state", "state", controlplane.PlanActionNoop)
	if err := ctx.Err(); err != nil {
		recorder.StepFailed(saveStep, err)
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return
	}
	if err := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
		recorder.StepFailed(saveStep, err)
		recorder.Finish(err)
		e.bridge.Finish(run, fmt.Errorf("save state: %w", err))
		return
	}
	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.StepDone(saveStep)
	recorder.MarkResourceStateRecorded()
	_, _ = log.Write([]byte("Apply complete.\n"))
	e.bridge.Finish(run, nil)
}

func (e *Executor) executeDestroy(ctx context.Context, run *controlplane.Run, log io.Writer) {
	unlock := e.bridge.LockTopology(run.Topology)
	defer unlock()

	if err := ctx.Err(); err != nil {
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
	st, err := mgr.Load()
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	if len(st.Resources) == 0 {
		_, _ = log.Write([]byte("Nothing to destroy.\n"))
		e.bridge.Finish(run, nil)
		return
	}
	plan := defaultDestroyPlan(st)
	if hook, ok := e.bridge.(DestroyHook); ok {
		plan, err = hook.BuildDestroyPlan(st)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		if err := hook.BeforeDestroy(plan); err != nil {
			e.bridge.Finish(run, err)
			return
		}
	}
	exec := runtime.NewExecutor(graph.New(), st)
	exec.SetRunContext(run.Topology, run.ID)
	exec.SetLogger(log)
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath).WithContext(ctx)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	exec.SetStatePatchSink(&runtime.StatePatchManagerSink{Manager: mgr, State: st, Owner: run.LeaseOwner})
	if snap, err := mgr.Snapshot(ctx, "before destroy "+run.ID); err == nil && snap != nil {
		_, _ = log.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Destroy(ctx, plan); err != nil {
		if saveErr := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: run.LeaseOwner}); saveErr != nil {
			_, _ = log.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
		} else {
			recorder.SetStateSerialAfter(st.Meta.Serial)
		}
		e.bridge.Finish(run, err)
		return
	}
	saveStep := recorder.StepStartKind("state", "state", controlplane.PlanActionNoop)
	if err := ctx.Err(); err != nil {
		recorder.StepFailed(saveStep, err)
		recorder.Finish(err)
		e.bridge.Finish(run, err)
		return
	}
	if err := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
		recorder.StepFailed(saveStep, err)
		recorder.Finish(err)
		e.bridge.Finish(run, fmt.Errorf("save state: %w", err))
		return
	}
	recorder.SetStateSerialAfter(st.Meta.Serial)
	recorder.StepDone(saveStep)
	recorder.MarkResourceStateRecorded()
	_, _ = log.Write([]byte("Destroy complete.\n"))
	e.bridge.Finish(run, nil)
}

func (e *Executor) executeResumeApply(ctx context.Context, parent, run *controlplane.Run, log io.Writer) {
	if err := e.bridge.ReconcileParentJournal(parent, run); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	e.executeApply(ctx, run, log)
}

func (e *Executor) executeResumeDestroy(ctx context.Context, parent, run *controlplane.Run, log io.Writer) {
	if err := e.bridge.ReconcileParentJournal(parent, run); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	e.executeDestroy(ctx, run, log)
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
