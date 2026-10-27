package agentexec

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type cancellationTask struct{ cancelled bool }

func (*cancellationTask) Start(context.Context, substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	return substrate.ExecutionHandle{}, nil
}
func (*cancellationTask) ObserveExecution(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return substrate.ExecResult{Status: substrate.ExecStatusRunning}, nil
}
func (c *cancellationTask) CancelExecution(context.Context, substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	c.cancelled = true
	return substrate.RemoteTerminationUnknown, nil
}
func (*cancellationTask) ReadExecutionOutput(context.Context, substrate.ExecutionHandle, string, int64, int) (substrate.ExecutionOutput, error) {
	return substrate.ExecutionOutput{}, nil
}
func (*cancellationTask) RenewExecution(context.Context, substrate.ExecutionHandle) error { return nil }
func (*cancellationTask) GuestTaskBootID(context.Context) (string, error)                 { return "boot", nil }
func TestGrantedTaskCancellationDoesNotInventStoppedEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &cancellationTask{}
	now := time.Now()
	grant := controlplane.SignedExecutionGrant{IssuedAt: now, Grant: controlplane.ExecutionGrant{ExpiresAt: now.Add(time.Minute)}}
	result, err := awaitGrantedTask(ctx, Options{}, client, substrate.ExecutionHandle{}, "", "", grant, func(controlplane.SignedExecutionGrant) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, client.cancelled)
	require.False(t, result.ExitCodeKnown)
	require.Equal(t, substrate.RemoteTerminationUnknown, result.RemoteTermination)
}

func TestGrantedTaskRequiresSeparateSupervisorWithoutFallback(t *testing.T) {
	_, err := runGrantedGuestTask(context.Background(), Options{}, controlplane.AgentCommand{})
	require.ErrorContains(t, err, "host supervisor socket")
}

func TestGrantedTaskRejectsMissingScopeBeforeSupervisorDial(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "public.key")
	require.NoError(t, os.WriteFile(key, make([]byte, ed25519.PublicKeySize), 0600))
	opts := Options{HostSupervisorSocket: filepath.Join(dir, "missing.sock"), ExecutionSessionID: "session", ExecutionGrantPublicKeyFile: key}
	cmd := controlplane.AgentCommand{Type: "guest_execution", ActionID: "action", ResourceUID: "node-uid", Generation: 1, Execution: &controlplane.GuestExecution{Topology: "lab", Node: "web"}, ExecutionRequest: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
	_, err := runGrantedGuestTask(context.Background(), opts, cmd)
	require.ErrorIs(t, err, guesttask.ErrConflict)
}

func TestGrantedTaskPassesExactScopeToSupervisorAndHonorsRejection(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "public.key")
	require.NoError(t, os.WriteFile(key, make([]byte, ed25519.PublicKeySize), 0600))
	socket := filepath.Join(dir, "supervisor.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	defer listener.Close()
	requests := make(chan hostsupervisor.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request hostsupervisor.Request
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		requests <- request
		json.NewEncoder(conn).Encode(hostsupervisor.Response{Version: 1, Error: "resource generation changed"})
	}()
	opts := Options{HostSupervisorSocket: socket, ExecutionSessionID: "session", ExecutionGrantPublicKeyFile: key}
	cmd := controlplane.AgentCommand{Type: "guest_execution", ActionID: "action", TopologyUID: "topology-uid", ResourceUID: "old-node-uid", Generation: 3, Execution: &controlplane.GuestExecution{Topology: "lab", Node: "web"}, ExecutionRequest: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = runGrantedGuestTask(ctx, opts, cmd)
	require.ErrorContains(t, err, "resource generation changed")
	request := <-requests
	require.Equal(t, "info", request.Operation)
	require.Equal(t, hostsupervisor.TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "topology-uid", ResourceUID: "old-node-uid", Generation: 3}}, request.Target)
}
