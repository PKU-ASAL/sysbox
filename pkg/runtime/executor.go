package runtime

import (
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"strings"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Executor wires graph walking to provider calls. It holds references to
// registered substrates (via substrate.Get) and updates state after each action.
type Executor struct {
	graph *graph.Graph
	state *state.State
}

func NewExecutor(g *graph.Graph, s *state.State) *Executor {
	return &Executor{graph: g, state: s}
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
	case "sysbox_monitor":
		return e.createMonitor(ctx, node)
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
	case "sysbox_monitor":
		return e.destroyMonitor(r)
	default:
		fmt.Printf("[destroy] skipping unimplemented resource type %q (%s)\n", r.Type, r.Name)
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
	if ref == "" {
		return "", fmt.Errorf("empty substrate ref")
	}
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 1:
		return parts[0], nil
	case 3:
		return parts[1], nil
	default:
		return "", fmt.Errorf("unexpected substrate ref %q", ref)
	}
}



// vethName produces a deterministic ≤15-char interface name.
// Format: <prefix>-<5hexhash>-<idx>  e.g. "vh-a3f2c-0"
// Uses SHA-1 for low collision probability even with large for_each counts.
func vethName(prefix, nodeName string, idx int) string {
	h := sha1.Sum([]byte(nodeName))
	hi := uint(h[0])<<8 | uint(h[1])
	return fmt.Sprintf("%s-%05x-%d", prefix, hi&0xfffff, idx)
}

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

// dockerSubstrate returns the registered Docker substrate as a DockerCapable
// interface for use in operations that need Docker-specific methods.
func (e *Executor) dockerSubstrate() (substrate.DockerCapable, error) {
	sub, err := substrate.Get("docker")
	if err != nil {
		return nil, err
	}
	dockerCap, ok := sub.(substrate.DockerCapable)
	if !ok {
		return nil, fmt.Errorf("docker substrate does not implement DockerCapable, got %T", sub)
	}
	return dockerCap, nil
}
