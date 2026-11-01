package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/stretchr/testify/require"
)

func TestRemotePowerRequiresSupervisorWithoutStateAccess(t *testing.T) {
	result := executeGrantedNodeOperation(context.Background(), Options{}, controlplane.AgentCommand{Operation: controlplane.NodeOperation{ID: "op", Operation: "pause"}})
	require.Equal(t, controlplane.NodeOperationStatusFailed, result.Status)
	require.Contains(t, result.Err, "host supervisor socket")
	require.Len(t, result.Audit, 2)
}

func TestRemotePowerRunnerNeverLoadsLocalProviderState(t *testing.T) {
	runner := newCommandRunner(Options{})
	cmd := controlplane.AgentCommand{ID: "command", Type: "node_operation", Operation: controlplane.NodeOperation{ID: "op", Topology: "lab", Node: "web", Operation: "pause"}}
	var terminal controlplane.AgentCommandEvent
	runner.Execute(context.Background(), &cmd, func(event controlplane.AgentCommandEvent) { terminal = event })
	require.Equal(t, "failed", terminal.Status)
	require.Contains(t, terminal.Error, "host supervisor socket")
}

func TestRemotePowerRejectsIncompleteScopeBeforeDial(t *testing.T) {
	cmd := controlplane.AgentCommand{ID: "command", Type: "node_operation", AgentID: "agent", ActionID: "action", ResourceUID: "resource", Generation: 1, Operation: controlplane.NodeOperation{Topology: "lab", Node: "web", Operation: "pause"}}
	result := executeGrantedNodeOperation(context.Background(), Options{HostSupervisorSocket: "/not-used.sock"}, cmd)
	require.Equal(t, controlplane.NodeOperationStatusFailed, result.Status)
	require.Contains(t, result.Err, "immutable target proof")
}

func TestRemotePowerLostResponseObservesWithoutReplayingEffect(t *testing.T) {
	dir := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(key, public, 0600))
	cmd := controlplane.AgentCommand{ID: "command", Type: "node_operation", AgentID: "agent", ActionID: "action", TopologyUID: "topology", ResourceUID: "resource", Generation: 1, Attempt: 1, LeaseOwner: "lease", Operation: controlplane.NodeOperation{ID: "op", Topology: "lab", Node: "web", Operation: "pause"}}
	request, err := controlplane.FromNodeOperationCommand(cmd)
	require.NoError(t, err)
	digest, err := request.Digest()
	require.NoError(t, err)
	grant, err := controlplane.SignExecutionGrant(private, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "action", Attempt: 1, Epoch: 1, Operation: "node_power", Digest: digest, State: controlplane.GrantActive, TargetBootID: "hostboot", ExpiresAt: time.Now().Add(time.Minute)}, "hostboot", time.Now())
	require.NoError(t, err)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(grant) }))
	defer api.Close()
	socket := filepath.Join(dir, "supervisor.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	defer listener.Close()
	seen := make(chan []string, 1)
	go func() {
		var calls []string
		for range 3 {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var r hostsupervisor.Request
			if json.NewDecoder(conn).Decode(&r) != nil {
				conn.Close()
				return
			}
			calls = append(calls, r.Operation)
			switch r.Operation {
			case "host-info":
				json.NewEncoder(conn).Encode(hostsupervisor.Response{Version: 1, BootID: "hostboot"})
			case "power-observe":
				json.NewEncoder(conn).Encode(hostsupervisor.Response{Version: 1})
			}
			conn.Close()
		}
		seen <- calls
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	op := executeGrantedNodeOperation(ctx, Options{ID: "agent", APIURL: api.URL, ExecutionSessionID: "session", ExecutionGrantPublicKeyFile: key, HostSupervisorSocket: socket}, cmd)
	require.Equal(t, controlplane.NodeOperationStatusDone, op.Status, op.Err)
	require.Equal(t, []string{"host-info", "power-apply", "power-observe"}, <-seen)
}
