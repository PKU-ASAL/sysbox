package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type runWorkspaceKey struct{}
type pinnedRunWorkspace struct {
	run  controlplane.Run
	path string
}

// WithRunWorkspace binds a trusted execution-host materialization to the exact
// canonical Run incarnation, preserving its API snapshot identity.
func WithRunWorkspace(ctx context.Context, run controlplane.Run, path string) context.Context {
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
