package agentexec

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInboxBlocksReplayAcrossProcessIncarnations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox", "command.json")
	cmd := &controlplane.AgentCommand{ID: "command", Type: "guest_execution", Run: &controlplane.Run{Inputs: map[string]string{"secret": "never-persist"}}}
	require.NoError(t, acceptCommand(path, "process-one", cmd))
	require.ErrorContains(t, acceptCommand(path, "process-two", cmd), "outcome unconfirmed")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), "never-persist")
}
func TestInboxStorageFailureCannotAcknowledge(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-directory"), []byte("x"), 0600))
	require.Error(t, acceptCommand(filepath.Join(dir, "not-directory", "record"), "process", &controlplane.AgentCommand{ID: "command"}))
}

func TestMissingParentCannotFallBackToFreshApply(t *testing.T) {
	bridge := NewLocalBridge(LocalOptions{RunsDir: t.TempDir(), ConfigFile: "missing.hcl"})
	run := &controlplane.Run{ID: "child", Topology: "topo", ParentID: "parent", Op: "apply"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
	require.Equal(t, controlplane.RunFailed, run.Status)
	require.Contains(t, run.Err, "parent runs require")
}

func TestDuplicateRunCommandCannotReplaceCancellationOwner(t *testing.T) {
	runner := newCommandRunner(nil, Options{})
	first := &controlplane.AgentCommand{ID: "first", Run: &controlplane.Run{ID: "run"}}
	second := &controlplane.AgentCommand{ID: "second", Run: &controlplane.Run{ID: "run"}}
	firstCancelled := false
	require.True(t, runner.track(first, func() { firstCancelled = true }))
	require.False(t, runner.track(second, func() { t.Error("second run cannot own cancellation") }))
	require.True(t, runner.cancel("run"))
	require.True(t, firstCancelled)
}

func TestRunAcknowledgesOnlyAfterInboxAndDurableClaim(t *testing.T) {
	bridge := NewLocalBridge(LocalOptions{RunsDir: t.TempDir(), ConfigFile: "missing.hcl"})
	cmd := &controlplane.AgentCommand{ID: "cmd", Type: "run_assigned", LeaseOwner: "delivery-owner", Run: &controlplane.Run{ID: "run"}}
	statuses := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, statuses, "claim must precede ACK/start")
		_, err := os.Stat(bridge.CheckpointFile("_agent_commands", commandInboxKey(cmd.ID)))
		require.NoError(t, err, "inbox must be durable before requesting execution authority")
		_ = json.NewEncoder(w).Encode(controlplane.Run{ID: "run", Op: "apply", Topology: "topo", AgentID: "agent", LeaseOwner: "process:claim", LeaseUntil: time.Now().Add(time.Minute), Status: controlplane.RunRunning, Attempt: 1})
	}))
	defer api.Close()
	runner := newCommandRunner(NewExecutorWithBridge(bridge), Options{ID: "agent", APIURL: api.URL, RunRenewInterval: time.Second})
	runner.Execute(context.Background(), cmd, func(event controlplane.AgentCommandEvent) { statuses = append(statuses, event.Status) })
	require.Equal(t, []string{"ack", "started", "failed"}, statuses)
}
