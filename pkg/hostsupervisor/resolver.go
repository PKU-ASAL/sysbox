package hostsupervisor

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
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
	raw, path, err := persistence.ResolveLocation(r.Backend, filepath.Join(r.RunsDir, ref.Topology, "state.json"), ref.Topology)
	if err != nil {
		return Target{}, err
	}
	if err := validateRepositoryTrust(raw, path, ref.Topology); err != nil {
		return Target{}, err
	}
	repo, err := persistence.OpenConfigured(ctx, raw, path)
	if err != nil {
		return Target{}, err
	}
	defer repo.Close()
	st, err := repo.LoadState(ctx, ref.Topology)
	if err != nil {
		return Target{}, err
	}
	if st.TopologyUID != ref.Scope.TopologyUID {
		return Target{}, fmt.Errorf("supervisor topology identity changed")
	}
	resource := st.FindResource(address.Resource("sysbox_node", ref.Node))
	if resource == nil {
		resource = st.FindResource(address.Resource("sysbox_router", ref.Node))
	}
	if resource == nil || resource.OwnerUID != st.TopologyUID || resource.ResourceUID != ref.Scope.ResourceUID || resource.Generation != ref.Scope.Generation {
		return Target{}, fmt.Errorf("supervisor resource identity changed")
	}
	node, err := driver.DefaultRegistry.RequireNode(resource.Driver)
	if err != nil {
		return Target{}, err
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(resource.Driver)
	if err != nil {
		return Target{}, err
	}
	handle, err := resource.ReconstructHandle(codec)
	if err != nil {
		return Target{}, err
	}
	identity := substrate.OperationIdentity{ActionID: "supervisor-observe", TopologyUID: st.TopologyUID, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation}
	observed, err := node.ObserveNode(substrate.WithOperationIdentity(ctx, identity), handle)
	if err != nil {
		return Target{}, err
	}
	if !observed.Exists || substrate.ValidateNodeObservationIdentity(identity, observed) != nil {
		return Target{}, fmt.Errorf("supervisor cannot verify actual node identity")
	}
	conn, err := node.Connection(handle, nil)
	if err != nil {
		return Target{}, err
	}
	return Target{Scope: ref.Scope, AgentID: r.AgentID, NodeID: handle.ID, Connection: conn}, nil
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
