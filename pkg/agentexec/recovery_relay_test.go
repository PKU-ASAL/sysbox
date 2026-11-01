package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/hostexec"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type recoveryTestBridge struct {
	hostexec.Bridge
	repo runtime.ActionRepository
}

func (b recoveryTestBridge) ActionRepository(string) (runtime.ActionRepository, error) {
	return b.repo, nil
}

func (b recoveryTestBridge) MaterializeRunRevision(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected revision materialization during observation")
}

type recoveryTestRepo struct {
	st      *state.State
	actions []runtime.ActionRecord
}

func (r *recoveryTestRepo) LoadState(context.Context, string) (*state.State, error) { return r.st, nil }
func (r *recoveryTestRepo) LoadActions(context.Context, string) ([]runtime.ActionRecord, error) {
	return r.actions, nil
}
func (r *recoveryTestRepo) CommitActionIntent(context.Context, runtime.ActionRecord) error {
	return errors.New("unexpected write")
}
func (r *recoveryTestRepo) CommitActionOutcome(context.Context, runtime.ActionRecord, *runtime.StatePatch) error {
	return errors.New("unexpected write")
}
func TestAgentRecoveryRunnerReturnsGenerationWithoutRunningEffects(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	binding := controlplane.FenceObservationBinding{RequestSHA256: strings.Repeat("a", 64), HostBootID: "11111111-1111-4111-8111-111111111111"}

	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunRunning, Attempt: 1, QueuedAt: time.Now().UTC(), Revision: "pinned"}
	repo := &recoveryTestRepo{st: &state.State{TopologyUID: "owner"}, actions: []runtime.ActionRecord{{ID: "action", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "web"), OwnerUID: "owner", ResourceUID: "resource", Generation: 7, Status: runtime.ActionUncertain}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/agents/agent/commands/observe/recovery-authorize", r.URL.Path)
		_ = json.NewEncoder(w).Encode(controlplane.RunRecoveryAuthorization{Run: run, Mode: "observe", FenceObservation: &binding, LeaseUntil: time.Now().Add(time.Minute)})
	}))
	defer server.Close()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	socket := filepath.Join(dir, "host.sock")
	host := &hostexec.RunExecutor{Bridge: recoveryTestBridge{repo: repo}, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent", HostBootID: binding.HostBootID, HostKey: key}, Authority: hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: server.URL}}}
	supervisor := &hostsupervisor.Server{Socket: socket, GroupID: -1, RunRecovery: host.Recover}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", socket)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}, time.Second, time.Millisecond)
	// No bridge at all on the worker: provider/state access belongs to host.
	runner := newCommandRunner(Options{HostSupervisorSocket: socket, APIURL: server.URL, ID: "agent", Policy: config.DefaultServiceConfig().Agent.Policy})
	cmd := controlplane.AgentCommand{ID: "observe", AgentID: "agent", ActionID: "fence-request", Type: controlplane.RunRecoveryCommand, Run: &run, RecoveryMode: "observe", LeaseOwner: "delivery", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute)}
	var events []controlplane.AgentCommandEvent
	runner.Execute(context.Background(), &cmd, func(e controlplane.AgentCommandEvent) { events = append(events, e) })
	require.Len(t, events, 2)
	last := events[1]
	require.Equal(t, "completed", last.Status, last.Error)
	require.Equal(t, cmd.ActionID, last.ActionID)
	require.NotNil(t, last.Recovery)
	require.NoError(t, controlplane.VerifyFenceObservation(pub, *last.Recovery, binding, cmd.LeaseOwner, cmd.Attempt))
	require.Len(t, last.Recovery.Resources, 1)
	require.EqualValues(t, 7, last.Recovery.Resources[0].Generation)
	require.Equal(t, "unknown", last.Recovery.Resources[0].Status)
	host.Tasks.HostBootID = "22222222-2222-4222-8222-222222222222"
	_, err = host.Recover(context.Background(), hostsupervisor.RunRecoveryRequest{CommandID: cmd.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt})
	require.ErrorContains(t, err, "boot")
	// Reusing the same Run ID must not relabel an older journal incarnation.
	run.QueuedAt = run.QueuedAt.Add(time.Hour)
	cmd.Run = &run
	_, err = hostexec.NewExecutorWithBridge(recoveryTestBridge{repo: repo}).ObserveRecoverRun(context.Background(), "agent", cmd, controlplane.RunRecoveryAuthorization{Run: run, Mode: "observe"})
	require.ErrorContains(t, err, "incarnation")
}
func TestRemoteRecoveryWithoutSupervisorNeverUsesWorkerBridge(t *testing.T) {
	runner := newCommandRunner(Options{ID: "agent", Policy: config.DefaultServiceConfig().Agent.Policy})
	cmd := controlplane.AgentCommand{ID: "observe", AgentID: "agent", Type: controlplane.RunRecoveryCommand, RecoveryMode: "observe", Run: &controlplane.Run{ID: "run", Topology: "lab"}, LeaseOwner: "delivery", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute)}
	var events []controlplane.AgentCommandEvent
	runner.Execute(context.Background(), &cmd, func(event controlplane.AgentCommandEvent) { events = append(events, event) })
	require.NotEmpty(t, events)
	require.Equal(t, "failed", events[len(events)-1].Status)
	require.Contains(t, events[len(events)-1].Error, "socket is not configured")
}
