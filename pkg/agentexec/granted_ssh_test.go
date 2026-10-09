package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSHAccessCommandRequiresBoundPayload(t *testing.T) {
	cmd := controlplane.AgentCommand{Type: "node_operation", ID: "command", AgentID: "agent", TopologyUID: "topology", ActionID: "action", ResourceUID: "resource", Generation: 1, Operation: controlplane.NodeOperation{Operation: "ssh_access_apply", SSHAccess: &controlplane.SSHAccessRequest{ID: "different", Topology: "lab", Node: "web", AuthorizedKeys: []string{"ssh-ed25519 AAAA key"}}}}
	_, err := controlplane.FromSSHAccessCommand(cmd)
	require.Error(t, err)
}

func TestSSHWorkerDoesNotPostCanonicalCompletionOnHostFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	for _, operation := range []string{"ssh_access_apply", "ssh_access_delete"} {
		runner := newCommandRunner(NewExecutorWithBridge(nil), Options{APIURL: server.URL, ID: "agent", Policy: config.DefaultServiceConfig().Agent.Policy})
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", ActionID: "action", ResourceUID: "node", Generation: 1, LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute), Operation: controlplane.NodeOperation{ID: "operation", Topology: "lab", Node: "web", Operation: operation}}
		var events []controlplane.AgentCommandEvent
		runner.Execute(context.Background(), &cmd, func(event controlplane.AgentCommandEvent) { events = append(events, event) })
		require.NotEmpty(t, events)
		require.Equal(t, "failed", events[len(events)-1].Status)
		require.Contains(t, events[len(events)-1].Error, "host supervisor")
	}
	require.Zero(t, calls.Load(), "worker must not manufacture a terminal operation report")
}
