package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// HostRunBridge supplies canonical claims and immutable revisions. Its Finish
// implementation must keep results local until the host signs completion.
type HostRunBridge interface {
	Bridge
	MaterializeRunRevision(context.Context, string, string) (string, error)
}

// HostRunExecutor is trusted host code, invoked only inside RunCoordinator's
// signed admission boundary. Workers cannot provide callbacks or provider paths.
type HostRunExecutor struct {
	Bridge    HostRunBridge
	Tasks     *hostsupervisor.Dispatcher
	Resolver  hostsupervisor.CanonicalResolver
	Authority hostsupervisor.AuthorityClient
}

func (h *HostRunExecutor) Execute(ctx context.Context, req hostsupervisor.RunRequest) (json.RawMessage, error) {
	if h.Bridge == nil || h.Tasks == nil {
		return nil, fmt.Errorf("host run executor is not configured")
	}
	if req.ExpectedSerial < 0 {
		return nil, fmt.Errorf("host run requires a nonnegative expected state serial")
	}
	run, err := h.Bridge.ParentRun(ctx, req.RunID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.Topology != req.Topology || run.Revision != req.Revision || run.Op != req.Action || run.Target != req.Target || run.AgentID != h.Tasks.AgentID || run.Status != controlplane.RunRunning || run.LeaseOwner == "" || !run.LeaseUntil.After(time.Now()) {
		return nil, fmt.Errorf("host run canonical claim changed")
	}
	path, err := h.Bridge.MaterializeRunRevision(ctx, req.Topology, req.Revision)
	if err != nil {
		return nil, fmt.Errorf("materialize immutable host revision: %w", err)
	}
	manager, err := h.Bridge.StateManager(req.Topology)
	if err != nil {
		return nil, err
	}
	canonical, err := manager.LoadWithContext(ctx)
	if err != nil || canonical == nil || canonical.TopologyUID != req.TopologyUID || canonical.Meta.Serial != req.ExpectedSerial {
		return nil, fmt.Errorf("host run canonical state changed")
	}
	if req.ResourceUID != "" {
		addr, parseErr := address.Parse(req.Target)
		if parseErr != nil {
			return nil, parseErr
		}
		resource := canonical.FindResource(addr)
		if resource == nil || resource.OwnerUID != req.TopologyUID || resource.ResourceUID != req.ResourceUID || resource.Generation != req.Generation {
			return nil, fmt.Errorf("host run target identity changed")
		}
	}
	childDispatcher := *h.Tasks
	childDispatcher.RunID = req.RunID
	childDispatcher.ResolveRun = h.Resolver.ResolveRunTask
	childDispatcher.Resolve = func(taskCtx context.Context, ref hostsupervisor.TargetRef) (hostsupervisor.Target, error) {
		return h.Resolver.ResolveRunTask(taskCtx, ref, req.RunID)
	}
	children := &hostsupervisor.RunChildren{Dispatcher: &childDispatcher, RenewGrant: h.Authority.RenewDerivedGuestGrant}
	runCtx := transport.WithTaskStartHandler(ctx, func(taskCtx context.Context, taskID string, taskReq substrate.ExecRequest, identity substrate.OperationIdentity) (substrate.ExecutionHandle, error) {
		parent, ok := transport.ParentExecutionGrantFromContext(taskCtx)
		if !ok {
			return substrate.ExecutionHandle{}, fmt.Errorf("host run parent grant unavailable")
		}
		ref, err := h.Resolver.FindRunTaskRef(taskCtx, req.Topology, req.RunID, controlplane.ExecutionScope{TopologyUID: identity.TopologyUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation})
		if err != nil {
			return substrate.ExecutionHandle{}, err
		}
		bootID, err := childDispatcher.BootID(taskCtx, ref)
		if err != nil {
			return substrate.ExecutionHandle{}, err
		}
		task, err := transport.BuildTaskRequest(taskCtx, taskID, taskReq)
		if err != nil {
			return substrate.ExecutionHandle{}, err
		}
		digest, err := task.Digest()
		if err != nil {
			return substrate.ExecutionHandle{}, err
		}
		task.Grant, err = h.Authority.DeriveGuestGrant(taskCtx, parent, taskID, digest, identity.ResourceUID, identity.Generation, bootID)
		if err != nil {
			return substrate.ExecutionHandle{}, err
		}
		// The child belongs to the Run, not a transient transport request.
		return children.Start(ctx, ref, task)
	})
	NewExecutorWithBridge(h.Bridge).ExecuteContext(WithRunWorkspace(runCtx, *run, path), run)
	if run.Status != controlplane.RunDone {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = children.Cancel(cleanup)
		if run.Op == "reset" {
			// Reacquire the same execution exclusion after the synchronous
			// executor returned. Unknown provider phases must stay uncertain.
			unlock, lockErr := h.Bridge.LockTopology(cleanup, run.Topology)
			if lockErr != nil {
				return nil, lockErr
			}
			defer unlock()
			original := run
			if run.ParentID != "" {
				original, err = h.Bridge.ParentRun(cleanup, run.ParentID)
				if err != nil {
					return nil, err
				}
			}
			provider, ok := h.Bridge.(ActionRepositoryProvider)
			if !ok {
				return nil, fmt.Errorf("reset stop requires action repository")
			}
			repo, err := provider.ActionRepository(run.Topology)
			if err != nil {
				return nil, err
			}
			actions, err := repo.LoadActions(cleanup, run.Topology)
			if err != nil {
				return nil, err
			}
			return nil, hostsupervisor.FinishReturnedResetFailure(cleanup, children, run, original, actions)
		}
		return nil, fmt.Errorf("host run failed: %s", run.Err)
	}
	if err := hostsupervisor.CheckpointRunChildren(ctx, children, run); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = children.Cancel(cleanup)
		return nil, fmt.Errorf("host Run resource checkpoint: %w", err)
	}
	if err := hostsupervisor.FinishRunChildren(ctx, children, run); err != nil {
		return nil, fmt.Errorf("host Run child outcome uncertain: %w", err)
	}
	return json.Marshal(run)
}
