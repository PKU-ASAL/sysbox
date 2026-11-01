package transport

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestTerminalTaskPreservesSignedRequest(t *testing.T) {
	opts := &substrate.TerminalOptions{TTY: true, Cols: 100, Rows: 40}
	task, err := BuildTaskRequest(context.Background(), "console", substrate.ExecRequest{Program: "/bin/sh", Shell: substrate.ShellNone, Terminal: opts})
	require.NoError(t, err)
	require.Equal(t, opts, task.Terminal)
}

func TestTerminalControlRequiresExactTaskGrant(t *testing.T) {
	h := substrate.ExecutionHandle{ID: "console", Digest: "digest", NodeID: "node", Generation: 1, BootID: "boot", Epoch: 3, TopologyUID: "topology", ResourceUID: "resource"}
	g := controlplane.SignedExecutionGrant{BootID: h.BootID, Grant: controlplane.ExecutionGrant{ActionID: h.ID, Digest: h.Digest, Epoch: h.Epoch, Scope: controlplane.ExecutionScope{TopologyUID: h.TopologyUID, ResourceUID: h.ResourceUID, Generation: h.Generation}}}
	calls := 0
	client := NewTaskRPCClient("node", 1, func(_ context.Context, call guesttask.Call) (guesttask.Response, error) {
		calls++
		require.Equal(t, "terminal-control", call.Operation)
		require.Equal(t, h.ID, call.ID)
		require.Equal(t, g, *call.Grant)
		return guesttask.Response{Version: guesttask.Version}, nil
	})
	control := substrate.TerminalControl{Type: "stdin", Data: []byte("hello\n")}
	require.Error(t, client.ControlExecution(context.Background(), h, control))
	require.NoError(t, client.ControlExecution(WithTaskGrant(context.Background(), h.ID, g), h, control))
	h.Digest = "changed"
	require.Error(t, client.ControlExecution(WithTaskGrant(context.Background(), h.ID, g), h, control))
	require.Equal(t, 1, calls)
}
