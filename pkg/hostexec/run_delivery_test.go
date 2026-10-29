package hostexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestResumeUsesLocalJournalWithoutParentRunMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte("# no resources"), 0600))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: path, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir})
	run := controlplane.Run{ID: "resume", ParentID: "only-in-api", Topology: "lab", Op: "apply", Status: controlplane.RunRunning}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), &run)
	require.Equal(t, controlplane.RunDone, run.Status, run.Err)
}

func TestRunWorkspaceLocalPathDoesNotRewriteCanonicalBinding(t *testing.T) {
	run := controlplane.Run{ID: "run", Revision: "digest", SnapshotPath: "/api-only/field.sysbox.hcl"}
	ctx := WithRunWorkspace(context.Background(), run, "/agent-only/field.sysbox.hcl")
	require.Equal(t, "/agent-only/field.sysbox.hcl", RunWorkspace(ctx, &run))
	require.Equal(t, "/api-only/field.sysbox.hcl", run.SnapshotPath)
	other := run
	other.Revision = "other"
	require.Empty(t, RunWorkspace(ctx, &other), "a local materialization must not authorize another revision")
}
