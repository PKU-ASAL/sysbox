package transport

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRPCClientLostReplyRetainsSignedRecoveryIdentity(t *testing.T) {
	req := substrate.ExecRequest{Program: "cmd.exe", Args: []string{"/c", "exit 0"}, Shell: substrate.ShellNone}
	task, err := BuildTaskRequest(context.Background(), "task", req)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	grant := controlplane.SignedExecutionGrant{BootID: "windows-boot", Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "top", ResourceUID: "resource", Generation: 3}, Epoch: 7, Digest: digest}}
	client := NewTaskRPCClient("uuid", 3, func(_ context.Context, c guesttask.Call) (guesttask.Response, error) {
		require.Equal(t, "start", c.Operation)
		require.Equal(t, digest, c.Request.Grant.Grant.Digest)
		return guesttask.Response{}, errors.New("QGA reply lost")
	})
	handle, err := client.Start(WithTaskGrant(context.Background(), task.ID, grant), req)
	require.Error(t, err)
	require.Equal(t, "uuid", handle.NodeID)
	require.Equal(t, "windows-boot", handle.BootID)
	require.Equal(t, digest, handle.Digest)
	require.Equal(t, uint64(7), handle.Epoch)
}
