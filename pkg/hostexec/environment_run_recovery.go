package hostexec

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (b *Environment) PinnedRevisionWorkspace(ctx context.Context, run controlplane.Run, revision *controlplane.GlobalRevision) (string, error) {
	if revision == nil || revision.Revision != run.Revision || controlplane.ComputeRevisionDigest(revision.Files) != run.Revision {
		return "", persistence.ErrConflict
	}
	return b.Workspaces.MaterializeRevision(run.Topology, run.Revision, revision.Files)
}
