package agentexec

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type pagedTaskOutput struct {
	durableGuestTaskConnection
	streams   map[string]string
	cursors   map[string][]int64
	badCursor bool
}

func (c *pagedTaskOutput) ObserveExecution(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return substrate.ExecResult{ExitCodeKnown: true, ExitCode: 7, Status: substrate.ExecStatusExited, RemoteTermination: substrate.RemoteTerminationConfirmed, StderrTruncated: true}, nil
}
func (c *pagedTaskOutput) ReadExecutionOutput(_ context.Context, _ substrate.ExecutionHandle, stream string, cursor int64, limit int) (substrate.ExecutionOutput, error) {
	if limit < 1 || limit > 64<<10 {
		return substrate.ExecutionOutput{}, fmt.Errorf("guest output page limit exceeded: %d", limit)
	}
	c.cursors[stream] = append(c.cursors[stream], cursor)
	data := c.streams[stream]
	if cursor > int64(len(data)) {
		return substrate.ExecutionOutput{}, fmt.Errorf("unexpected cursor")
	}
	end := min(len(data), int(cursor)+limit)
	out := substrate.ExecutionOutput{Data: []byte(data[cursor:end]), NextCursor: int64(end)}
	if c.badCursor {
		out.NextCursor = cursor
	}
	return out, nil
}
func TestGrantedTaskReadsOutputInBoundedPages(t *testing.T) {
	for _, size := range []int{0, 64 << 10, 150000, controlplane.GuestOutputLimit, controlplane.GuestOutputLimit + 100} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			client := &pagedTaskOutput{streams: map[string]string{"stdout": strings.Repeat("x", size), "stderr": "error"}, cursors: map[string][]int64{}}
			grant := controlplane.SignedExecutionGrant{IssuedAt: time.Now(), Grant: controlplane.ExecutionGrant{ExpiresAt: time.Now().Add(time.Minute)}}
			got, err := awaitGrantedTask(context.Background(), Options{}, client, substrate.ExecutionHandle{}, "", "", grant, nil)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("x", min(size, controlplane.GuestOutputLimit)), got.Stdout)
			require.Equal(t, "error", got.Stderr)
			require.Equal(t, size > controlplane.GuestOutputLimit, got.StdoutTruncated)
			require.True(t, got.StderrTruncated, "guest-side truncation survives collection")
			require.Equal(t, 7, got.ExitCode)
			for i, cursor := range client.cursors["stdout"] {
				require.EqualValues(t, i*(64<<10), cursor)
			}
		})
	}
}
func TestGrantedTaskRejectsNonAdvancingOutputCursor(t *testing.T) {
	client := &pagedTaskOutput{streams: map[string]string{"stdout": "data"}, cursors: map[string][]int64{}, badCursor: true}
	grant := controlplane.SignedExecutionGrant{IssuedAt: time.Now(), Grant: controlplane.ExecutionGrant{ExpiresAt: time.Now().Add(time.Minute)}}
	_, err := awaitGrantedTask(context.Background(), Options{}, client, substrate.ExecutionHandle{}, "", "", grant, nil)
	require.ErrorContains(t, err, "cursor")
}
