package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

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

type resolverTaskCaller struct{}

func TestParentExecutionGrantReadsCurrentAuthority(t *testing.T) {
	old := controlplane.SignedExecutionGrant{Grant: controlplane.ExecutionGrant{Epoch: 7}}
	current := old
	valid := true
	ctx := WithParentExecutionGrant(context.Background(), old)
	ctx = WithParentExecutionGrantResolver(ctx, func() (controlplane.SignedExecutionGrant, bool) { return current, valid })
	current.Grant.ExpiresAt = time.Now().Add(time.Minute)
	got, ok := ParentExecutionGrantFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, current, got)
	valid = false
	_, ok = ParentExecutionGrantFromContext(ctx)
	require.False(t, ok, "revoked dynamic authority must not fall back to the original grant")
}

func (resolverTaskCaller) taskCall(_ context.Context, call guesttask.Call) (guesttask.Response, error) {
	if call.Request == nil {
		return guesttask.Response{}, nil
	}
	digest, _ := call.Request.Digest()
	return guesttask.Response{Record: &guesttask.Record{ID: call.Request.ID, Digest: digest, BootID: "guest", PID: 42}}, nil
}

func TestTaskStartDerivesGrantForHostRunContext(t *testing.T) {
	req := substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone}
	identity := substrate.OperationIdentity{ActionID: "run", OwnerUID: "topology", TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
	called := false
	ctx := substrate.WithOperationIdentity(context.Background(), identity)
	ctx = WithTaskGrantResolver(ctx, func(ctx context.Context, id string, request substrate.ExecRequest, got substrate.OperationIdentity) (controlplane.SignedExecutionGrant, error) {
		called = true
		task, err := BuildTaskRequest(ctx, id, request)
		if err != nil {
			return controlplane.SignedExecutionGrant{}, err
		}
		digest, err := task.Digest()
		if err != nil {
			return controlplane.SignedExecutionGrant{}, err
		}
		return controlplane.SignedExecutionGrant{BootID: "guest", Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: got.TopologyUID, ResourceUID: got.ResourceUID, Generation: got.Generation}, Digest: digest, Epoch: 1, Operation: "guest_task", ActionID: id}}, nil
	})
	caller := resolverTaskCaller{}
	handle, err := startTask(ctx, caller, executionIdentity{nodeID: "node", generation: 1}, req)
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, "guest", handle.BootID)
	require.NotEmpty(t, handle.ID)
}
