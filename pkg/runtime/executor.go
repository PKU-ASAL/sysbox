package runtime

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Executor wires graph walking to provider calls. It holds references to
// registered substrates (via substrate.Get) and updates state after each action.
type Executor struct {
	graph               *graph.Graph
	state               *state.State
	logger              io.Writer
	recorder            OperationRecorder
	patchSink           StatePatchSink
	topology            string
	runID               string
	currentResourceStep int
}

func NewExecutor(g *graph.Graph, s *state.State) *Executor {
	return &Executor{graph: g, state: s, logger: os.Stdout, recorder: NoopRecorder{}, currentResourceStep: -1}
}

// SetLogger redirects all apply/destroy log output (e.g. to capture it for
// an API SSE stream). Defaults to os.Stdout.
func (e *Executor) SetLogger(w io.Writer) { e.logger = w }

func (e *Executor) SetRecorder(r OperationRecorder) {
	if r == nil {
		e.recorder = NoopRecorder{}
		return
	}
	e.recorder = r
}

func (e *Executor) SetStatePatchSink(sink StatePatchSink) {
	e.patchSink = sink
}

func (e *Executor) SetRunContext(topology, runID string) {
	e.topology = topology
	e.runID = runID
}

func (e *Executor) recordSubstep(parent int, phase string, details map[string]any, fn func() error) error {
	step := e.recorder.SubstepStart(parent, phase, details)
	err := fn()
	if err != nil {
		e.recorder.StepFailed(step, err)
		return err
	}
	e.recorder.StepDone(step)
	return nil
}

func (e *Executor) substepHook(parent int) NICWireHook {
	if parent < 0 {
		return nil
	}
	return func(phase string, details map[string]any, fn func() error) error {
		return e.recordSubstep(parent, phase, details, fn)
	}
}

func (e *Executor) setCurrentResourceStep(step int) func() {
	prev := e.currentResourceStep
	e.currentResourceStep = step
	return func() {
		e.currentResourceStep = prev
	}
}

func (e *Executor) recordStepExternal(step int, id graph.NodeID, action PlanActionType) {
	r := e.state.FindResource(id.Type, id.Name)
	if r == nil {
		return
	}
	externalID := r.Str("id")
	if p, ok := GetResourceProvider(r.Type); ok {
		externalID = p.ExternalID(*r)
	}
	e.recorder.StepExternal(step, r.Provider, externalID, ManagedLabels(e.topology, e.runID, id))
	log := StateResourceLog{
		Type:     r.Type,
		Name:     r.Name,
		Provider: r.Provider,
		Instance: r.Instance,
	}
	e.recorder.StepStateResource(step, log)
	patch := StatePatch{
		Index:    step,
		Resource: id.String(),
		Action:   action,
		Op:       StatePatchUpsert,
		State:    &log,
	}
	e.recorder.StepStatePatch(step, StatePatchUpsert, &log)
	if e.patchSink != nil {
		if err := e.patchSink.ApplyStatePatch(context.Background(), patch); err != nil {
			e.logf("[state] warning: persist patch for %s: %v\n", id, err)
		} else {
			e.recorder.StepStateRecorded(step)
		}
	}
}

func (e *Executor) recordDeletePatch(step int, r state.Resource, action PlanActionType) {
	log := StateResourceLog{
		Type:     r.Type,
		Name:     r.Name,
		Provider: r.Provider,
		Instance: r.Instance,
	}
	e.recorder.StepStatePatch(step, StatePatchDelete, &log)
	if e.patchSink != nil {
		patch := StatePatch{
			Index:    step,
			Resource: r.Type + "." + r.Name,
			Action:   action,
			Op:       StatePatchDelete,
			State:    &log,
		}
		if err := e.patchSink.ApplyStatePatch(context.Background(), patch); err != nil {
			e.logf("[state] warning: persist delete patch for %s.%s: %v\n", r.Type, r.Name, err)
		} else {
			e.recorder.StepStateRecorded(step)
		}
	}
}

func (e *Executor) logf(format string, args ...any) {
	fmt.Fprintf(e.logger, format, args...)
}

// CreateResource dispatches a node in the graph to the right provider
// and records the result in state.
func (e *Executor) CreateResource(ctx context.Context, id graph.NodeID) error {
	node := e.graph.Get(id.Type, id.Name)
	if node == nil {
		return fmt.Errorf("node %s not in graph", id)
	}

	if p, ok := GetResourceProvider(id.Type); ok {
		res, err := p.Create(ctx, &ProviderContext{exec: e}, node)
		if err != nil {
			return err
		}
		e.state.AddResource(res)
		return nil
	}

	return nil
}

// DestroyResource tears down a resource listed in state.
func (e *Executor) DestroyResource(ctx context.Context, r state.Resource) error {
	if p, ok := GetResourceProvider(r.Type); ok {
		return p.Delete(ctx, &ProviderContext{exec: e}, r)
	}

	e.logf("[destroy] skipping unimplemented resource type %q (%s)\n", r.Type, r.Name)
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- reference resolution helpers --
//
// After HCL EvalContext lands, references decode to bare strings:
//
//	substrate.docker.light    -> "docker"
//	sysbox_image.alpine.id    -> "alpine"
//
// We still accept legacy "type.name.attr" quoted strings for backwards
// compatibility with HCL files that don't use traversals.

func resolveSubstrateRef(ref string) (string, error) {
	return config.ResolveSubstrateRef(ref)
}

// stateAdapter wraps *state.State to implement substrate.StateReader.
type stateAdapter struct{ st *state.State }

func (a stateAdapter) ResourceInstance(typ, name string) map[string]any {
	r := a.st.FindResource(typ, name)
	if r == nil {
		return nil
	}
	return r.Instance
}

var _ substrate.StateReader = stateAdapter{}

// expandTilde replaces a leading ~ with the current user's home directory.
func expandTilde(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}
