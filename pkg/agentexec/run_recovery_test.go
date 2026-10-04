package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type recoveryTestBridge struct {
	Bridge
	repo runtime.ActionRepository
}

func (b recoveryTestBridge) ActionRepository(string) (runtime.ActionRepository, error) {
	return b.repo, nil
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
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Revision: "pinned"}
	repo := &recoveryTestRepo{st: &state.State{TopologyUID: "owner"}, actions: []runtime.ActionRecord{{ID: "action", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "web"), OwnerUID: "owner", ResourceUID: "resource", Generation: 7, Status: runtime.ActionUncertain}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/agents/agent/commands/observe/recovery-authorize", r.URL.Path)
		_ = json.NewEncoder(w).Encode(controlplane.RunRecoveryAuthorization{Run: run, Mode: "observe"})
	}))
	defer server.Close()
	runner := newCommandRunner(NewExecutorWithBridge(recoveryTestBridge{repo: repo}), Options{APIURL: server.URL, ID: "agent", Policy: config.DefaultServiceConfig().Agent.Policy})
	cmd := controlplane.AgentCommand{ID: "observe", AgentID: "agent", Type: controlplane.RunRecoveryCommand, Run: &run, RecoveryMode: "observe", LeaseOwner: "delivery", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute)}
	var events []controlplane.AgentCommandEvent
	runner.Execute(context.Background(), &cmd, func(e controlplane.AgentCommandEvent) { events = append(events, e) })
	require.Len(t, events, 2)
	last := events[1]
	require.Equal(t, "completed", last.Status, last.Error)
	require.NotNil(t, last.Recovery)
	require.Len(t, last.Recovery.Resources, 1)
	require.EqualValues(t, 7, last.Recovery.Resources[0].Generation)
	require.Equal(t, "unknown", last.Recovery.Resources[0].Status)
	// Reusing the same Run ID must not relabel an older journal incarnation.
	run.QueuedAt = run.QueuedAt.Add(time.Hour)
	cmd.Run = &run
	_, err := runner.executor.ObserveRecoverRun(context.Background(), "agent", cmd, controlplane.RunRecoveryAuthorization{Run: run, Mode: "observe"})
	require.ErrorContains(t, err, "incarnation")
}
