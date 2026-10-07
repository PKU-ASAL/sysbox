package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type revisionDeliveryBridge struct {
	Bridge
	called bool
}

func (b *revisionDeliveryBridge) PinnedRevisionWorkspace(_ context.Context, _ controlplane.Run, _ *controlplane.GlobalRevision) (string, error) {
	b.called = true
	return "/agent/field.sysbox.hcl", nil
}

func TestRunDeliveryRejectsTamperedRevisionBeforeMaterializing(t *testing.T) {
	files := map[string][]byte{"field.sysbox.hcl": []byte("# original")}
	digest := controlplane.ComputeRevisionDigest(files)
	for _, tamper := range []bool{false, true} {
		t.Run(fmt.Sprint(tamper), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received := controlplane.Run{}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
				require.Equal(t, digest, received.Revision)
				result := controlplane.GlobalRevision{Revision: digest, Files: files}
				if tamper {
					result.Files = map[string][]byte{"field.sysbox.hcl": []byte("# altered")}
				}
				require.NoError(t, json.NewEncoder(w).Encode(result))
			}))
			defer server.Close()
			bridge := &revisionDeliveryBridge{}
			runner := newCommandRunner(NewExecutorWithBridge(bridge), Options{APIURL: server.URL, ID: "agent"})
			run := controlplane.Run{ID: "run", Revision: digest, SnapshotPath: "/api-only/field.sysbox.hcl"}
			ctx, err := runner.prepareRunWorkspace(context.Background(), run)
			if tamper {
				require.ErrorContains(t, err, "digest mismatch")
				require.False(t, bridge.called)
			} else {
				require.NoError(t, err)
				require.True(t, bridge.called)
				require.Equal(t, "/agent/field.sysbox.hcl", RunWorkspace(ctx, &run))
			}
		})
	}
}

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
	ctx := withRunWorkspace(context.Background(), run, "/agent-only/field.sysbox.hcl")
	require.Equal(t, "/agent-only/field.sysbox.hcl", RunWorkspace(ctx, &run))
	require.Equal(t, "/api-only/field.sysbox.hcl", run.SnapshotPath)
	other := run
	other.Revision = "other"
	require.Empty(t, RunWorkspace(ctx, &other), "a local materialization must not authorize another revision")
}
