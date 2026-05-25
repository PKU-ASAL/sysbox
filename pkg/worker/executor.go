package worker

import (
	"context"
	"fmt"
	"io"

	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
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
}

type Executor struct {
	bridge Bridge
}

func NewExecutor(cfg config.ServiceConfig) *Executor {
	return NewExecutorWithBridge(api.NewExecutionBridge(cfg))
}

func NewExecutorWithBridge(bridge Bridge) *Executor {
	return &Executor{bridge: bridge}
}

func (e *Executor) Execute(run *controlplane.Run) {
	e.bridge.AttachRun(run)
	log := e.bridge.LogWriter(run.ID)
	switch run.Op {
	case "apply":
		if run.ParentID != "" {
			if parent, err := e.bridge.ParentRun(context.Background(), run.ParentID); err == nil {
				e.executeResumeApply(parent, run, log)
				return
			}
		}
		e.executeApply(run, log)
	case "destroy":
		if run.ParentID != "" {
			if parent, err := e.bridge.ParentRun(context.Background(), run.ParentID); err == nil {
				e.executeResumeDestroy(parent, run, log)
				return
			}
		}
		e.executeDestroy(run, log)
	default:
		e.bridge.Finish(run, fmt.Errorf("unsupported run op %q", run.Op))
	}
}

func (e *Executor) executeApply(run *controlplane.Run, log io.Writer) {
	unlock := e.bridge.LockTopology(run.Topology)
	defer unlock()

	if err := e.bridge.Preflight(context.Background(), run.Topology, log); err != nil {
		e.bridge.Finish(run, err)
		return
	}

	mgr, err := e.bridge.StateManager(run.Topology)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	g, mgr, st, _, _, err := runtime.LoadWorkspaceWithManager(e.bridge.HCLFile(run.Topology), mgr)
	if err != nil {
		e.bridge.Finish(run, err)
		return
	}
	meta, _ := mgr.Metadata(context.Background())
	var plan *runtime.Plan
	if run.PlanID != "" {
		stored, err := e.bridge.ValidateStoredPlanForApply(context.Background(), run.Topology, run.PlanID, meta.Serial)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
		plan = runtime.PlanFromActions(stored.Actions, st)
	} else {
		plan, err = runtime.ComputePlan(g, st)
		if err != nil {
			e.bridge.Finish(run, err)
			return
		}
	}
	exec := runtime.NewExecutor(g, st)
	exec.SetRunContext(run.Topology, run.ID)
	exec.SetLogger(log)
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	exec.SetStatePatchSink(&runtime.StatePatchManagerSink{Manager: mgr, State: st, Owner: run.LeaseOwner})
	if run.PlanID == "" {
		exec.Refresh(context.Background(), plan)
	}
	if !plan.HasChanges() {
		_, _ = log.Write([]byte("No changes. Apply is a no-op.\n"))
		e.bridge.Finish(run, nil)
		return
	}
	_, _ = log.Write([]byte(plan.Summary() + "\n"))
	if snap, err := mgr.Snapshot(context.Background(), "before apply "+run.ID); err == nil && snap != nil {
		_, _ = log.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Apply(context.Background(), plan); err != nil {
		if saveErr := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); saveErr != nil {
			_, _ = log.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
		} else {
			recorder.SetStateSerialAfter(st.Meta.Serial)
		}
		e.bridge.Finish(run, err)
		return
	}
	saveStep := recorder.StepStartKind("state", "state", runtime.PlanActionUpdate)
	if err := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
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

func (e *Executor) executeDestroy(run *controlplane.Run, log io.Writer) {
	unlock := e.bridge.LockTopology(run.Topology)
	defer unlock()

	mgr, err := e.bridge.StateManager(run.Topology)
	if err != nil {
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
	plan := &runtime.Plan{Destroy: append([]state.Resource(nil), st.Resources...)}
	for _, r := range plan.Destroy {
		plan.Actions = append(plan.Actions, runtime.PlanAction{
			Resource: r.Type + "." + r.Name,
			Type:     r.Type,
			Name:     r.Name,
			Action:   runtime.PlanActionDelete,
			Reason:   "destroy requested",
		})
	}
	exec := runtime.NewExecutor(graph.New(), st)
	exec.SetRunContext(run.Topology, run.ID)
	exec.SetLogger(log)
	checkpointPath := e.bridge.CheckpointFile(run.Topology, run.ID)
	fileRecorder := runtime.NewFileRecorder(checkpointPath, run.ID, run.Topology)
	recorder := runtime.NewStoreRecorder(fileRecorder, e.bridge.CheckpointStore(), run.Topology, run.ID, checkpointPath)
	recorder.SetLeaseOwner(run.LeaseOwner)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	exec.SetRecorder(recorder)
	exec.SetStatePatchSink(&runtime.StatePatchManagerSink{Manager: mgr, State: st, Owner: run.LeaseOwner})
	if snap, err := mgr.Snapshot(context.Background(), "before destroy "+run.ID); err == nil && snap != nil {
		_, _ = log.Write([]byte(fmt.Sprintf("State snapshot: %s\n", snap.ID)))
	}
	if err := exec.Destroy(context.Background(), plan); err != nil {
		if saveErr := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); saveErr != nil {
			_, _ = log.Write([]byte(fmt.Sprintf("warning: save state failed: %v\n", saveErr)))
		} else {
			recorder.SetStateSerialAfter(st.Meta.Serial)
		}
		e.bridge.Finish(run, err)
		return
	}
	saveStep := recorder.StepStartKind("state", "state", runtime.PlanActionUpdate)
	if err := mgr.SaveWithLease(context.Background(), st, state.LockOptions{Owner: run.LeaseOwner}); err != nil {
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

func (e *Executor) executeResumeApply(parent, run *controlplane.Run, log io.Writer) {
	if err := e.bridge.ReconcileParentJournal(parent, run); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	e.executeApply(run, log)
}

func (e *Executor) executeResumeDestroy(parent, run *controlplane.Run, log io.Writer) {
	if err := e.bridge.ReconcileParentJournal(parent, run); err != nil {
		e.bridge.Finish(run, err)
		return
	}
	e.executeDestroy(run, log)
}
