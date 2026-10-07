package hostsupervisor

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalResolver uses supervisor-configured repository locations. No worker
// request can supply a host path, provider private state or network endpoint.
type CanonicalResolver struct{ RunsDir, Backend, AgentID string }

func (r CanonicalResolver) Resolve(ctx context.Context, ref TargetRef) (Target, error) {
	target, err := r.ResolveNode(ctx, ref)
	if err != nil {
		return Target{}, err
	}
	conn, err := target.Node.Connection(target.Handle, nil)
	if err != nil {
		return Target{}, err
	}
	return Target{Scope: target.Scope, AgentID: target.AgentID, NodeID: target.Handle.ID, User: target.Handle.Conn.User, Connection: conn}, nil
}

// NodeTarget stays inside the privileged process. It never crosses the worker
// socket and does not require a guest connection (a paused guest cannot reply).
type NodeTarget struct {
	Scope   controlplane.ExecutionScope
	AgentID string
	Handle  substrate.NodeHandle
	Node    driver.Node
	Power   driver.Power
}

func (r CanonicalResolver) ResolveNode(ctx context.Context, ref TargetRef) (NodeTarget, error) {
	var zero NodeTarget
	raw, path, err := persistence.ResolveLocation(r.Backend, filepath.Join(r.RunsDir, ref.Topology, "state.json"), ref.Topology)
	if err != nil {
		return zero, err
	}
	if err := validateRepositoryTrust(raw, path, ref.Topology); err != nil {
		return zero, err
	}
	repo, err := persistence.OpenConfigured(ctx, raw, path)
	if err != nil {
		return zero, err
	}
	defer repo.Close()
	st, err := repo.LoadState(ctx, ref.Topology)
	if err != nil {
		return zero, err
	}
	if st.TopologyUID != ref.Scope.TopologyUID {
		return zero, fmt.Errorf("supervisor topology identity changed")
	}
	resource := st.FindResource(address.Resource("sysbox_node", ref.Node))
	if resource == nil {
		resource = st.FindResource(address.Resource("sysbox_router", ref.Node))
	}
	if resource == nil || resource.OwnerUID != st.TopologyUID || resource.ResourceUID != ref.Scope.ResourceUID || resource.Generation != ref.Scope.Generation {
		return zero, fmt.Errorf("supervisor resource identity changed")
	}
	node, err := driver.DefaultRegistry.RequireNode(resource.Driver)
	if err != nil {
		return zero, err
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(resource.Driver)
	if err != nil {
		return zero, err
	}
	handle, err := resource.ReconstructHandle(codec)
	if err != nil {
		return zero, err
	}
	identity := substrate.OperationIdentity{ActionID: "supervisor-observe", TopologyUID: st.TopologyUID, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation}
	observed, err := node.ObserveNode(substrate.WithOperationIdentity(ctx, identity), handle)
	if err != nil {
		return zero, err
	}
	if !observed.Exists || substrate.ValidateNodeObservationIdentity(identity, observed) != nil {
		return zero, fmt.Errorf("supervisor cannot verify actual node identity")
	}
	power, _ := driver.DefaultRegistry.RequirePower(resource.Driver)
	return NodeTarget{Scope: ref.Scope, AgentID: r.AgentID, Handle: handle, Node: node, Power: power}, nil
}

func validateRepositoryTrust(backend, path, topology string) error {
	if backend == "" {
		backend = path
	}
	if strings.HasPrefix(backend, "postgres://") || strings.HasPrefix(backend, "postgresql://") {
		// SQL role isolation is a deployment requirement: the worker must not
		// possess credentials that can modify this canonical repository.
		return nil
	}
	if strings.HasPrefix(backend, "sqlite://") {
		file, err := persistence.SQLitePath(backend)
		if err != nil {
			return err
		}
		if err := ValidateOwnedPath(file, true); err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(file + suffix); err == nil {
				if err := ValidateOwnedPath(file+suffix, true); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	if strings.Contains(backend, "://") {
		u, err := url.Parse(backend)
		if err != nil {
			return err
		}
		if u.Scheme != "file" && u.Scheme != "local" {
			return fmt.Errorf("unsupported supervisor repository")
		}
		backend = u.Path
		if backend == "" {
			backend = u.Host
		}
	}
	root := backend + ".lifecycle"
	if err := ValidateOwnedPath(root, true); err != nil {
		return err
	}
	return ValidateOwnedPath(filepath.Join(root, topology+".json"), true)
}
