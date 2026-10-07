package api

import (
	"context"
	"io"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestExecutionPreflightUsesOnlyAdmittedSnapshot(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	bridge := NewExecutionBridge(cfg)
	files := map[string][]byte{"field.sysbox.hcl": []byte("# immutable empty project\n")}
	revision := controlplane.ComputeRevisionDigest(files)
	path, err := bridge.server.workspaceService().MaterializeRevision("lab", revision, files)
	require.NoError(t, err)
	run := &controlplane.Run{Topology: "lab", Revision: revision, SnapshotPath: path}
	require.NoError(t, bridge.Preflight(context.Background(), run, io.Discard), "a mutable workspace is not required for keyed admission")
	run.SnapshotPath = ""
	require.ErrorContains(t, bridge.Preflight(context.Background(), run, io.Discard), "immutable snapshot")
}
