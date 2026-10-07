package agentexec

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type runWorkspaceKey struct{}
type pinnedRunWorkspace struct {
	run  controlplane.Run
	path string
}

func withRunWorkspace(ctx context.Context, run controlplane.Run, path string) context.Context {
	return context.WithValue(artifact.WithPortableSources(ctx), runWorkspaceKey{}, pinnedRunWorkspace{run: run, path: path})
}

// RunWorkspace returns the execution-host materialization without changing the
// canonical snapshot binding used by claims, journals and completion receipts.
func RunWorkspace(ctx context.Context, run *controlplane.Run) string {
	if run == nil {
		return ""
	}
	if local, ok := ctx.Value(runWorkspaceKey{}).(pinnedRunWorkspace); ok {
		if !persistence.SameRunIncarnation(local.run, *run) {
			return ""
		}
		return local.path
	}
	return run.SnapshotPath
}

func (r *commandRunner) prepareRunWorkspace(ctx context.Context, run controlplane.Run) (context.Context, error) {
	workspace, ok := r.executor.bridge.(RevisionWorkspaceProvider)
	if !ok {
		return ctx, fmt.Errorf("agent requires immutable revision materialization")
	}
	var revision controlplane.GlobalRevision
	if err := post(ctx, r.opts, r.opts.APIURL+"/v1/agents/"+r.opts.ID+"/runs/"+run.ID+"/revision", run, &revision); err != nil {
		return ctx, err
	}
	if revision.Revision != run.Revision || controlplane.ComputeRevisionDigest(revision.Files) != run.Revision {
		return ctx, fmt.Errorf("run revision digest mismatch")
	}
	path, err := workspace.PinnedRevisionWorkspace(ctx, run, &revision)
	if err != nil {
		return ctx, err
	}
	if path == "" {
		return ctx, fmt.Errorf("empty run revision materialization")
	}
	return withRunWorkspace(ctx, run, path), nil
}
