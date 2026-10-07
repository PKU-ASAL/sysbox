//go:build e2e

package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFirecrackerDurableGuestTaskE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key := filepath.Join(t.TempDir(), "public")
	require.NoError(t, os.WriteFile(key, public, 0600))
	s.grantPublicKeyFile = key
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "task-owner", TopologyUID: "task-topology", ResourceUID: "task-node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "task-e2e", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	require.NoError(t, conn.(substrate.ConnectionWaiter).WaitReady(ctx, 30*time.Second))
	boot, err := conn.(interface {
		GuestTaskBootID(context.Context) (string, error)
	}).GuestTaskBootID(ctx)
	require.NoError(t, err)
	request := substrate.ExecRequest{Program: "printf durable-output; sleep 1; exit 7", Shell: substrate.ShellLinux}
	task, err := transport.BuildTaskRequest(ctx, "durable", request)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "durable", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: digest, ExpiresAt: time.Now().UTC().Add(time.Minute), State: controlplane.GrantActive}
	signed, err := controlplane.SignExecutionGrant(private, grant, boot, time.Now().UTC())
	require.NoError(t, err)
	diagnostic, e := conn.Exec(ctx, substrate.ExecRequest{Program: "date -u +%Y-%m-%dT%H:%M:%S.%N; cat /proc/sys/kernel/random/boot_id; wc -c /etc/sysbox/grant-public-key; stat -fc %T /sys/fs/cgroup", Shell: substrate.ShellLinux}, nil, nil)
	require.NoError(t, e)
	t.Logf("host=%s issued=%s expires=%s guest=%s", time.Now().UTC(), signed.IssuedAt, signed.Grant.ExpiresAt, diagnostic.Stdout)
	authorized := transport.WithTaskGrant(ctx, "durable", signed)
	handle, err := conn.(substrate.ConnectionStarter).Start(authorized, request)
	require.NoError(t, err)
	duplicate, err := conn.(substrate.ConnectionStarter).Start(authorized, request)
	require.NoError(t, err)
	require.Equal(t, handle.ID, duplicate.ID)
	cold, err := s.Connection(h, nil)
	require.NoError(t, err)
	var result substrate.ExecResult
	require.Eventually(t, func() bool {
		result, err = cold.(substrate.ConnectionExecutionObserver).ObserveExecution(ctx, handle)
		return err == nil && result.ExitCodeKnown
	}, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, 7, result.ExitCode)
	output, err := cold.(substrate.ConnectionExecutionOutput).ReadExecutionOutput(ctx, handle, "stdout", 0, 100)
	require.NoError(t, err)
	require.Equal(t, "durable-output", string(output.Data))
	request.Program = "sleep 60 & wait"
	task, err = transport.BuildTaskRequest(ctx, "cancel", request)
	require.NoError(t, err)
	grant.Digest, err = task.Digest()
	require.NoError(t, err)
	grant.ActionID = "cancel"
	grant.CommandID = "cancel-command"
	grant.Epoch = 2
	signed, err = controlplane.SignExecutionGrant(private, grant, boot, time.Now().UTC())
	require.NoError(t, err)
	handle, err = cold.(substrate.ConnectionStarter).Start(transport.WithTaskGrant(ctx, "cancel", signed), request)
	require.NoError(t, err)
	termination, err := cold.(substrate.ConnectionExecutionCanceler).CancelExecution(ctx, handle)
	require.NoError(t, err)
	if termination != substrate.RemoteTerminationConfirmed {
		require.Eventually(t, func() bool {
			observation, err := cold.(substrate.ConnectionExecutionObserver).ObserveExecution(ctx, handle)
			return err == nil && observation.RemoteTermination == substrate.RemoteTerminationConfirmed
		}, 5*time.Second, 25*time.Millisecond)
	}
	t.Log("real guest signed task dedup, cold connection observe/output, and process-tree cancellation verified")
}
