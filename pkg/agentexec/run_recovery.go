package agentexec

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/secret"
	"path/filepath"
	"time"
)

type RevisionWorkspaceProvider interface {
	PinnedRevisionWorkspace(context.Context, controlplane.Run, *controlplane.GlobalRevision) (string, error)
}

func (e *Executor) ObserveRecoverRun(ctx context.Context, agentID string, cmd controlplane.AgentCommand, auth controlplane.RunRecoveryAuthorization) (*controlplane.RunRecoveryReport, error) {
	run := auth.Run
	if cmd.Run == nil || run.ID != cmd.Run.ID || run.AgentID != agentID || !run.QueuedAt.Equal(cmd.Run.QueuedAt) || run.Revision != cmd.Run.Revision || auth.Mode != cmd.RecoveryMode {
		return nil, fmt.Errorf("recovery authorization mismatch")
	}
	provider, ok := e.bridge.(ActionRepositoryProvider)
	if !ok {
		return nil, runtime.ErrConvergenceUnsupported
	}
	repo, err := provider.ActionRepository(run.Topology)
	if err != nil {
		return nil, err
	}
	repo = &incarnationRecoveryRepository{ActionRepository: repo, run: run}
	// The journal is the authority for this exact run incarnation, not a
	// reusable Run ID or the request's self-reported resource identity.
	journal, err := repo.LoadActions(ctx, run.Topology)
	if err != nil {
		return nil, err
	}
	if run.QueuedAt.IsZero() {
		return nil, fmt.Errorf("run incarnation unavailable")
	}
	for _, action := range journal {
		if action.RunID == run.ID && (!action.RunQueuedAt.Equal(run.QueuedAt) || action.RunRevision != run.Revision) {
			return nil, fmt.Errorf("journal run incarnation mismatch")
		}
	}
	if auth.Mode == controlplane.RunRecoveryRecover {
		if run.Status != controlplane.RunFailed || !run.Recoverable || run.LeaseUntil.After(time.Now()) {
			return nil, fmt.Errorf("live execution cannot be recovered")
		}
		workspace, ok := e.bridge.(RevisionWorkspaceProvider)
		if !ok {
			return nil, runtime.ErrConvergenceUnsupported
		}
		path, err := workspace.PinnedRevisionWorkspace(ctx, run, auth.Revision)
		if err != nil {
			return nil, err
		}
		ctx = artifact.WithPortableSources(ctx)
		mgr, err := e.bridge.StateManager(run.Topology)
		if err != nil {
			return nil, err
		}
		g, _, st, _, _, err := runtime.LoadWorkspaceWithInputs(path, mgr, nil)
		if err != nil {
			return nil, err
		}
		if err := runtime.ValidateArtifactSources(ctx, g, filepath.Dir(path), secret.EnvironmentResolver{}); err != nil {
			return nil, err
		}
		exec := runtime.NewExecutor(g, st)
		exec.SetRunContext(run.Topology, run.ID)
		exec.SetRunIncarnation(run.QueuedAt, run.Revision)
		exec.SetWorkspaceFromHCLFile(path)
		exec.SetActionRepository(repo)
		if err := exec.RecoverRunActions(ctx, run.ID); err != nil {
			return nil, err
		}
	} else if auth.Mode != controlplane.RunRecoveryObserve {
		return nil, fmt.Errorf("unsupported recovery mode")
	}
	st, resources, err := runtime.ObserveRunActions(ctx, repo, run.Topology, run.ID)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("recovery journal unavailable on owning agent")
	}
	return &controlplane.RunRecoveryReport{RunAttempt: run.Attempt, RunLeaseOwner: run.LeaseOwner, CommandID: cmd.ID, RunID: run.ID, AgentID: agentID, QueuedAt: run.QueuedAt, Revision: run.Revision, Mode: auth.Mode, TopologyUID: st.TopologyUID, StateSerial: st.Meta.Serial, ObservedAt: time.Now().UTC(), Resources: resources}, nil
}

// Every journal read, including the engine's later verification pass, checks
// incarnation. Completed older rounds are history; unfinished older rounds
// require explicit resolution and cannot be adopted as this run's work.
type incarnationRecoveryRepository struct {
	runtime.ActionRepository
	run controlplane.Run
}

func (r *incarnationRecoveryRepository) LoadActions(ctx context.Context, topology string) ([]runtime.ActionRecord, error) {
	records, err := r.ActionRepository.LoadActions(ctx, topology)
	if err != nil {
		return nil, err
	}
	out := []runtime.ActionRecord{}
	for _, record := range records {
		if record.RunID == r.run.ID && (!record.RunQueuedAt.Equal(r.run.QueuedAt) || record.RunRevision != r.run.Revision) {
			if record.Status == runtime.ActionSucceeded {
				continue
			}
			return nil, fmt.Errorf("journal run incarnation mismatch")
		}
		out = append(out, record)
	}
	return out, nil
}
func (b remoteBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	p, ok := b.Bridge.(ActionRepositoryProvider)
	if !ok {
		return nil, runtime.ErrConvergenceUnsupported
	}
	return p.ActionRepository(topology)
}
func (b remoteBridge) PinnedRevisionWorkspace(ctx context.Context, run controlplane.Run, revision *controlplane.GlobalRevision) (string, error) {
	p, ok := b.Bridge.(RevisionWorkspaceProvider)
	if !ok {
		return "", runtime.ErrConvergenceUnsupported
	}
	return p.PinnedRevisionWorkspace(ctx, run, revision)
}
