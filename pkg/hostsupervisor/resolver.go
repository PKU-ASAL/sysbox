package hostsupervisor

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalResolver uses supervisor-configured repository locations. No worker
// request can supply a host path, provider private state or network endpoint.
type CanonicalResolver struct {
	RunsDir, Backend, AgentID string
	// ActionRun resolves a trusted canonical execution to its admitted action
	// owner. It is configured by the host, never supplied by a task RPC.
	ActionRun func(context.Context, string, string) (string, error)
}

func (r CanonicalResolver) actionRun(ctx context.Context, topology, execution string) (string, error) {
	if r.ActionRun == nil {
		return execution, nil
	}
	owner, err := r.ActionRun(ctx, topology, execution)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", fmt.Errorf("canonical action Run is missing")
	}
	return owner, nil
}

func (r CanonicalResolver) Resolve(ctx context.Context, ref TargetRef) (Target, error) {
	target, err := r.ResolveNode(ctx, ref)
	if err != nil {
		return Target{}, err
	}
	return canonicalConnection(target)
}

// NodeTarget stays inside the privileged process. It never crosses the worker
// socket and does not require a guest connection (a paused guest cannot reply).
type NodeTarget struct {
	canonicalResource *state.Resource
	Scope             controlplane.ExecutionScope
	AgentID           string
	Handle            substrate.NodeHandle
	Node              driver.Node
	Power             driver.Power
}

func (r CanonicalResolver) ResolveNode(ctx context.Context, ref TargetRef) (NodeTarget, error) {
	var zero NodeTarget
	repo, err := r.openRepository(ctx, ref.Topology)
	if err != nil {
		return zero, err
	}
	defer repo.Close()
	st, err := repo.LoadState(ctx, ref.Topology)
	if err != nil {
		return zero, err
	}
	resource, err := selectRunTaskResource(st, nil, ref, "")
	if err != nil {
		return zero, err
	}
	return r.resolveResource(ctx, ref, resource)
}

func (r CanonicalResolver) openRepository(ctx context.Context, topology string) (*persistence.Repository, error) {
	raw, path, err := persistence.ResolveLocation(r.Backend, filepath.Join(r.RunsDir, topology, "state.json"), topology)
	if err != nil {
		return nil, err
	}
	if err := validateRepositoryTrust(raw, path, topology); err != nil {
		return nil, err
	}
	return persistence.OpenConfigured(ctx, raw, path)
}

// ResolveRunTask permits an authenticated Run's unfinished node checkpoint.
// runID must come from the supervisor's accepted parent Run, never an RPC field.
func (r CanonicalResolver) ResolveRunTask(ctx context.Context, ref TargetRef, runID string) (Target, error) {
	if runID == "" {
		return Target{}, fmt.Errorf("supervisor run identity is required")
	}
	runID, err := r.actionRun(ctx, ref.Topology, runID)
	if err != nil {
		return Target{}, err
	}
	repo, err := r.openRepository(ctx, ref.Topology)
	if err != nil {
		return Target{}, err
	}
	defer repo.Close()
	st, actions, err := repo.LoadStateAndActions(ctx, ref.Topology)
	if err != nil {
		return Target{}, err
	}
	resource, err := selectRunTaskResource(st, actions, ref, runID)
	if err != nil {
		return Target{}, err
	}
	node, err := r.resolveResource(ctx, ref, resource)
	if err != nil {
		return Target{}, err
	}
	return canonicalConnection(node)
}

// FindRunTaskRef derives the logical node name from trusted repository identity.
func (r CanonicalResolver) FindRunTaskRef(ctx context.Context, topology, runID string, scope controlplane.ExecutionScope) (TargetRef, error) {
	var zero TargetRef
	if runID == "" {
		return zero, fmt.Errorf("supervisor run identity is required")
	}
	runID, err := r.actionRun(ctx, topology, runID)
	if err != nil {
		return zero, err
	}
	repo, err := r.openRepository(ctx, topology)
	if err != nil {
		return zero, err
	}
	defer repo.Close()
	st, actions, err := repo.LoadStateAndActions(ctx, topology)
	if err != nil {
		return zero, err
	}
	names := map[string]bool{}
	for _, resource := range st.Resources {
		if resource.ResourceUID == scope.ResourceUID {
			names[resource.Address.Name] = true
		}
	}
	for _, action := range actions {
		if action.RunID == runID && action.ResourceUID == scope.ResourceUID {
			names[action.Resource.Name] = true
		}
	}
	var found *TargetRef
	for name := range names {
		ref := TargetRef{Topology: topology, Node: name, Scope: scope}
		if _, err := selectRunTaskResource(st, actions, ref, runID); err != nil {
			continue
		}
		if found != nil {
			return zero, fmt.Errorf("supervisor run node identity is ambiguous")
		}
		found = &ref
	}
	if found == nil {
		return zero, fmt.Errorf("supervisor run node identity not found")
	}
	return *found, nil
}

func selectRunTaskResource(st *state.State, actions []runtime.ActionRecord, ref TargetRef, runID string) (*state.Resource, error) {
	if st.TopologyUID == "" || st.TopologyUID != ref.Scope.TopologyUID {
		return nil, fmt.Errorf("supervisor topology identity changed")
	}
	matchesAddress := func(a address.Address) bool {
		return a.Equal(address.Resource("sysbox_node", ref.Node)) || a.Equal(address.Resource("sysbox_router", ref.Node))
	}
	matchesIdentity := func(r *state.Resource) bool {
		return r.OwnerUID == st.TopologyUID && r.ResourceUID == ref.Scope.ResourceUID && r.Generation == ref.Scope.Generation && r.Generation > 0
	}
	var current *state.Resource
	for i := range st.Resources {
		resource := &st.Resources[i]
		if !matchesAddress(resource.Address) {
			continue
		}
		if current != nil {
			return nil, fmt.Errorf("supervisor node address is ambiguous")
		}
		current = resource
	}
	if current != nil && matchesIdentity(current) {
		return current, nil
	}
	var candidate *state.Resource
	for _, action := range actions {
		if runID == "" || action.RunID != runID || action.Topology != ref.Topology || !matchesAddress(action.Resource) || (action.Status != runtime.ActionIntentCommitted && action.Status != runtime.ActionUncertain) || action.OwnerUID != st.TopologyUID || action.ResourceUID != ref.Scope.ResourceUID || action.Generation != ref.Scope.Generation {
			continue
		}
		if action.Action != controlplane.PlanActionCreate && action.Action != controlplane.PlanActionReset {
			continue
		}
		c := action.Candidate
		replacement := action.Action == controlplane.PlanActionReset && action.Phase == "applying" && !action.EffectComplete && action.Replacement != nil
		if replacement {
			c = action.Replacement
		}
		if c == nil || !c.Address.Equal(action.Resource) || !matchesIdentity(c) {
			continue
		}
		if current != nil && (action.Action != controlplane.PlanActionReset || (!action.EffectComplete && !replacement) || !current.Address.Equal(c.Address) || current.OwnerUID != c.OwnerUID || current.ResourceUID != c.ResourceUID || current.Generation+1 != c.Generation) {
			return nil, fmt.Errorf("supervisor canonical resource conflicts with candidate")
		}
		if candidate != nil {
			return nil, fmt.Errorf("supervisor run node candidate is ambiguous")
		}
		candidate = c
	}
	if candidate == nil {
		return nil, fmt.Errorf("supervisor resource identity changed or run candidate unavailable")
	}
	return candidate, nil
}

func (r CanonicalResolver) resolveResource(ctx context.Context, ref TargetRef, resource *state.Resource) (NodeTarget, error) {
	var zero NodeTarget
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
	identity := substrate.OperationIdentity{ActionID: "supervisor-observe", TopologyUID: ref.Scope.TopologyUID, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation}
	observed, err := node.ObserveNode(substrate.WithOperationIdentity(ctx, identity), handle)
	if err != nil {
		return zero, err
	}
	if !observed.Exists || substrate.ValidateNodeObservationIdentity(identity, observed) != nil {
		return zero, fmt.Errorf("supervisor cannot verify actual node identity")
	}
	power, _ := driver.DefaultRegistry.RequirePower(resource.Driver)
	return NodeTarget{Scope: ref.Scope, AgentID: r.AgentID, Handle: handle, Node: node, Power: power, canonicalResource: resource}, nil
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
