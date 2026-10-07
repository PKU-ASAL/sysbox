package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
)

func TestVsockLostTaskStartReturnsStableRecoveryHandle(t *testing.T) {
	received := make(chan guesttask.Call, 1)
	conn := transferPeer(t, func(_ net.Conn, r *bufio.Reader, req vsockrpc.Request) {
		if req.Op != vsockrpc.OpTask {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r, req.Size))
		if err != nil {
			return
		}
		var call guesttask.Call
		if json.Unmarshal(raw, &call) != nil {
			return
		}
		received <- call
		// Close after receiving the request, without acknowledging task launch.
	})
	conn.BindExecutionIdentity("node", 2)
	req := substrate.ExecRequest{Program: "sleep", Args: []string{"30"}, Shell: substrate.ShellNone}
	task, err := BuildTaskRequest(context.Background(), "request-id", req)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	ctx := WithTaskGrant(context.Background(), task.ID, controlplane.SignedExecutionGrant{BootID: "boot", Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{Generation: 2}, Digest: digest, Epoch: 9}})
	h, err := conn.Start(ctx, req)
	require.Error(t, err)
	require.Equal(t, task.ID, h.ID)
	require.Equal(t, digest, h.Digest)
	require.Equal(t, "boot", h.BootID)
	require.EqualValues(t, 9, h.Epoch)
	call := <-received
	require.Equal(t, "start", call.Operation)
	require.Equal(t, task.ID, call.Request.ID)
}

func TestTaskObservationRejectsDifferentBootAndEpoch(t *testing.T) {
	for _, mismatch := range []string{"boot", "epoch", "digest"} {
		t.Run(mismatch, func(t *testing.T) {
			record := guesttask.Record{ID: "task", Digest: "digest", BootID: "boot", Grant: guesttask.SignedGrant{Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{Generation: 1}, Epoch: 1}}}
			switch mismatch {
			case "boot":
				record.BootID = "other"
			case "epoch":
				record.Grant.Grant.Epoch = 2
			case "digest":
				record.Digest = "other"
			}
			conn := transferPeer(t, func(c net.Conn, r *bufio.Reader, req vsockrpc.Request) {
				_, _ = io.CopyN(io.Discard, r, req.Size)
				raw, _ := json.Marshal(guesttask.Response{Version: 1, Record: &record})
				_ = json.NewEncoder(c).Encode(vsockrpc.Frame{Done: true, Task: raw})
			})
			conn.BindExecutionIdentity("node", 1)
			_, err := conn.ObserveExecution(context.Background(), substrate.ExecutionHandle{ID: "task", Digest: "digest", BootID: "boot", NodeID: "node", Generation: 1, Epoch: 1})
			require.ErrorIs(t, err, guesttask.ErrConflict)
		})
	}
}
