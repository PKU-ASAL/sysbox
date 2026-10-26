package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

type pinnedHostRecoveryBridge struct {
	*observedAgentBridge
	path         string
	revision     string
	materialized bool
}

func (b *pinnedHostRecoveryBridge) RecoveryActionRepository(_ context.Context, _ hostsupervisor.RunRecoveryRequest, run controlplane.Run) (runtime.ActionRepository, error) {
	return b.ActionRepository(run.Topology)
}

func (b *pinnedHostRecoveryBridge) MaterializeRunRevision(context.Context, string, string) (string, error) {
	return b.path, nil
}
func (b *pinnedHostRecoveryBridge) PinnedRevisionWorkspace(_ context.Context, run controlplane.Run, revision *controlplane.GlobalRevision) (string, error) {
	if revision == nil || revision.Revision != b.revision || run.Revision != b.revision || controlplane.ComputeRevisionDigest(revision.Files) != b.revision {
		return "", fmt.Errorf("canonical revision mismatch")
	}
	b.materialized = true
	return b.path, nil
}

func TestHostRecoveryCommitsProvenJournalThroughSocketWithoutReplay(t *testing.T) {
	handler := &agentObservedResource{kind: fmt.Sprintf("host_recovery_resource_%d", observedResourceCounter.Add(1))}
	runtime.RegisterResourceHandler(handler)
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "field.sysbox.hcl")
	hcl := []byte(fmt.Sprintf(`variable "replicas" { type = number }
resource %q "node" { count = var.replicas }`, handler.kind))
	require.NoError(t, os.WriteFile(path, hcl, 0600))
	files := map[string][]byte{"field.sysbox.hcl": hcl}
	revision := controlplane.GlobalRevision{Files: files, Revision: controlplane.ComputeRevisionDigest(files)}
	local := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: path, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, Log: io.Discard})
	repo, err := local.ActionRepository("lab")
	require.NoError(t, err)
	fault := &responseLostRepository{ActionRepository: repo, failPatch: true}
	bridge := &pinnedHostRecoveryBridge{observedAgentBridge: &observedAgentBridge{LocalBridge: local, repository: fault}, path: path, revision: revision.Revision}
	run := &controlplane.Run{ID: "run", Inputs: map[string]string{"replicas": "1"}, AgentID: "agent", Topology: "lab", Op: "apply", Revision: revision.Revision, QueuedAt: time.Now().UTC()}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
	require.Equal(t, controlplane.RunFailed, run.Status)
	require.Equal(t, 1, handler.creates)
	before, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.Empty(t, before.Resources)
	fault.failPatch = false
	run.Recoverable = true
	run.LeaseUntil = time.Now().Add(-time.Minute)
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/agents/agent/commands/recover/recovery-authorize", r.URL.Path)
		_ = json.NewEncoder(w).Encode(controlplane.RunRecoveryAuthorization{Inputs: map[string]string{"replicas": "1"}, Run: *run, Mode: controlplane.RunRecoveryRecover, Revision: &revision, LeaseUntil: time.Now().Add(time.Minute)})
	}))
	defer authority.Close()
	host := &HostRunExecutor{Bridge: bridge, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent"}, Authority: hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: authority.URL}}}
	socket := filepath.Join(dir, "host.sock")
	supervisor := &hostsupervisor.Server{Socket: socket, GroupID: -1, RunRecovery: host.Recover}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, time.Second, time.Millisecond)
	client := hostsupervisor.NewTaskClient(socket, hostsupervisor.TargetRef{})
	report, err := client.RecoverRun(ctx, hostsupervisor.RunRecoveryRequest{CommandID: "recover", LeaseOwner: "worker", Attempt: 1})
	require.NoError(t, err)
	require.True(t, bridge.materialized)
	require.Equal(t, controlplane.RunRecoveryRecover, report.Mode)
	require.Len(t, report.Resources, 1)
	require.Equal(t, "present", report.Resources[0].Status)
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Len(t, after.Resources, 1)
	require.Greater(t, after.Meta.Serial, before.Meta.Serial)
	actions, err := repo.LoadActions(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, runtime.ActionSucceeded, actions[0].Status)
	require.Equal(t, 1, handler.creates, "recovery must not replay provider creation")
	require.Zero(t, handler.deletes)
}
