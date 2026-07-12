package runtime

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Executor wires graph walking to provider calls. It holds references to
// registered capability drivers and updates state after each action.
type Executor struct {
	graph               *graph.Graph
	state               *state.State
	logger              io.Writer
	recorder            OperationRecorder
	patchSink           StatePatchSink
	topology            string
	runID               string
	operation           string
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

func (e *Executor) SetOperation(operation string) {
	e.operation = operation
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

func (e *Executor) recordStepExternal(ctx context.Context, step int, id address.Address, action controlplane.PlanActionType) {
	r := e.state.FindResource(id)
	if r == nil {
		return
	}
	externalID := r.Str("id")
	if p, ok := GetResourceHandler(r.Address.Type); ok {
		externalID = p.ExternalID(*r)
	}
	e.recorder.StepExternal(step, r.Driver, externalID, ManagedLabels(e.topology, e.runID, id))
	log := StateResourceLog{
		Type:     r.Address.Type,
		Name:     r.Address.Name,
		Provider: r.Driver,
		Instance: r.AttributeMap(),
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
		if err := e.patchSink.ApplyStatePatch(ctx, patch); err != nil {
			e.logf("[state] warning: persist patch for %s: %v\n", id, err)
		} else {
			e.recorder.StepStateRecorded(step)
		}
	}
}

func (e *Executor) recordDeletePatch(ctx context.Context, step int, r state.Resource, action controlplane.PlanActionType) {
	log := StateResourceLog{
		Type:     r.Address.Type,
		Name:     r.Address.Name,
		Provider: r.Driver,
		Instance: r.AttributeMap(),
	}
	e.recorder.StepStatePatch(step, StatePatchDelete, &log)
	if e.patchSink != nil {
		patch := StatePatch{
			Index:    step,
			Resource: r.Address.String(),
			Action:   action,
			Op:       StatePatchDelete,
			State:    &log,
		}
		if err := e.patchSink.ApplyStatePatch(ctx, patch); err != nil {
			e.logf("[state] warning: persist delete patch for %s: %v\n", r.Address, err)
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
func (e *Executor) CreateResource(ctx context.Context, id address.Address) error {
	node := e.graph.Get(id)
	if node == nil {
		return fmt.Errorf("node %s not in graph", id)
	}

	if p, ok := GetResourceHandler(id.Type); ok {
		res, err := p.Create(ctx, &ProviderContext{exec: e}, node)
		if err != nil {
			return err
		}
		res.ResourceType = id.Type
		res.SchemaVersion = p.Schema().Version
		res.Dependencies = append([]address.Address(nil), node.Deps...)
		res.Status = state.ResourcePresent
		if res.ExternalID == "" {
			res.ExternalID = p.ExternalID(res)
		}
		e.state.AddResource(res)
		return nil
	}

	return nil
}

// DestroyResource tears down a resource listed in state.
func (e *Executor) DestroyResource(ctx context.Context, r state.Resource) error {
	if p, ok := GetResourceHandler(r.Address.Type); ok {
		return p.Delete(ctx, &ProviderContext{exec: e}, r)
	}

	e.logf("[destroy] skipping unimplemented resource %s\n", r.Address)
	e.state.RemoveResource(r.Address)
	return nil
}

// -- reference resolution helpers --
//
// After HCL EvalContext lands, references decode to bare strings:
//
//	substrate.docker.light    -> "docker"
//	sysbox_image.alpine.id    -> "alpine"
//
// Quoted "type.name.attr" strings are normalized here as reference literals.

func resolveSubstrateRef(ref string) (string, error) {
	return config.ResolveSubstrateRef(ref)
}

// stateAdapter wraps *state.State to implement substrate.StateReader.
type stateAdapter struct{ st *state.State }

func (a stateAdapter) ResourceInstance(addr address.Address) map[string]any {
	r := a.st.FindResource(addr)
	if r == nil {
		return nil
	}
	return r.AttributeMap()
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
