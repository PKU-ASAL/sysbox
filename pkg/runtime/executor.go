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
	graph    *graph.Graph
	state    *state.State
	logger   io.Writer
	recorder OperationRecorder
	topology string
	runID    string
}

func NewExecutor(g *graph.Graph, s *state.State) *Executor {
	return &Executor{graph: g, state: s, logger: os.Stdout, recorder: NoopRecorder{}}
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

func (e *Executor) recordStepExternal(step int, id graph.NodeID) {
	r := e.state.FindResource(id.Type, id.Name)
	if r == nil {
		return
	}
	externalID := resourceExternalID(*r)
	e.recorder.StepExternal(step, r.Provider, externalID, ManagedLabels(e.topology, e.runID, id))
	e.recorder.StepStateResource(step, StateResourceLog{
		Type:     r.Type,
		Name:     r.Name,
		Provider: r.Provider,
		Instance: r.Instance,
	})
}

func resourceExternalID(r state.Resource) string {
	switch r.Type {
	case "sysbox_node", "sysbox_router", "sysbox_actor":
		if id := r.ContainerID(); id != "" {
			return id
		}
	case "sysbox_network":
		if id := r.DockerNetID(); id != "" {
			return id
		}
		if ns := r.NetNS(); ns != "" {
			return ns
		}
	case "sysbox_image":
		if id := r.ImageID(); id != "" {
			return id
		}
	case "sysbox_kernel":
		return r.Str("path")
	}
	return r.Str("id")
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

	switch id.Type {
	case "sysbox_network":
		return e.createNetwork(ctx, node)
	case "sysbox_image":
		return e.createImage(ctx, node)
	case "sysbox_kernel":
		return e.createKernel(ctx, node)
	case "sysbox_node":
		return e.createNode(ctx, node)
	case "sysbox_router":
		return e.createRouter(ctx, node)
	case "sysbox_firewall":
		return e.createFirewall(ctx, node)
	case "sysbox_ssh_access":
		return e.createSSHAccess(ctx, node)
	case "sysbox_actor":
		return e.createActor(ctx, node)
	case "data_sysbox_node":
		return e.readDataNode(ctx, node)
	case "data_sysbox_network":
		return e.readDataNetwork(ctx, node)
	case "data_sysbox_image":
		return e.readDataImage(ctx, node)
	default:
		return nil
	}
}

// DestroyResource tears down a resource listed in state.
func (e *Executor) DestroyResource(ctx context.Context, r state.Resource) error {
	switch r.Type {
	case "sysbox_network":
		return e.destroyNetwork(ctx, r)
	case "sysbox_node":
		return e.destroyNode(ctx, r)
	case "sysbox_router":
		return e.destroyRouter(ctx, r)
	case "sysbox_image":
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	case "sysbox_kernel":
		// Cache files are content-addressed and shared; do not delete from disk.
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	case "sysbox_firewall":
		return e.destroyFirewall(ctx, r)
	case "sysbox_ssh_access":
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	case "sysbox_actor":
		return e.destroyActor(ctx, r)
	case "data_sysbox_node", "data_sysbox_network", "data_sysbox_image":
		// Data sources are read-only; nothing to destroy in the substrate.
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	default:
		e.logf("[destroy] skipping unimplemented resource type %q (%s)\n", r.Type, r.Name)
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}
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
