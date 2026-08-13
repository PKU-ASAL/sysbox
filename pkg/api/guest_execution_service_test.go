package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestGuestExecutionCreateValidatesRequest(t *testing.T) {
	svc := newGuestExecutionService(&localAPIStore{runsDir: t.TempDir()}, nil, nil)
	for _, tc := range []struct {
		name string
		req  controlplane.GuestExecutionRequest
	}{
		{"argv required", controlplane.GuestExecutionRequest{}},
		{"environment name", controlplane.GuestExecutionRequest{Argv: []string{"true"}, Environment: map[string]string{"BAD-NAME": "secret"}}},
		{"timeout bounded", controlplane.GuestExecutionRequest{Argv: []string{"true"}, TimeoutSeconds: 86401}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), "lab", "web", "host-a", tc.req)
			require.Error(t, err)
		})
	}
}

func TestGuestExecutionLifecyclePreservesNonZeroExit(t *testing.T) {
	ctx := context.Background()
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestExecutionService(store, nil, nil)
	exec, err := svc.Create(ctx, "lab", "web", "host-a", controlplane.GuestExecutionRequest{Argv: []string{"sh", "-c", "exit 7"}, TimeoutSeconds: 30})
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionQueued, exec.Status)

	exec, err = svc.MarkRunning(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionRunning, exec.Status)

	exec, err = svc.Complete(ctx, exec.ID, controlplane.GuestExecutionResult{ExitCode: 7, Stderr: "YmFk", Encoding: "base64"}, controlplane.GuestExecutionResultClassExit, "")
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCompleted, exec.Status)
	require.Equal(t, 7, exec.Result.ExitCode)
	require.Equal(t, controlplane.GuestExecutionResultClassExit, exec.ResultClass)
	_, err = svc.Complete(ctx, exec.ID, controlplane.GuestExecutionResult{}, "", "")
	require.ErrorContains(t, err, "terminal")

	raw, err := json.Marshal(exec)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "host-a")
	require.NotContains(t, string(raw), "environment")
}

func TestGuestExecutionCancelIsExplicitTerminalTransition(t *testing.T) {
	ctx := context.Background()
	var cancelTarget string
	svc := newGuestExecutionService(&localAPIStore{runsDir: t.TempDir()}, func(_ context.Context, _ string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		if cmd.Type == "cancel_command" {
			cancelTarget = cmd.Operation.ExternalID
		}
		return cmd, nil
	}, nil)
	exec, err := svc.Create(ctx, "lab", "web", "host-a", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "10"}})
	require.NoError(t, err)
	exec, err = svc.Cancel(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCancelled, exec.Status)
	require.Equal(t, exec.ID, cancelTarget)
	require.WithinDuration(t, time.Now().UTC(), exec.EndedAt, time.Second)
	_, err = svc.Complete(ctx, exec.ID, controlplane.GuestExecutionResult{ExitCode: 0}, controlplane.GuestExecutionResultClassExit, "")
	require.ErrorIs(t, err, errGuestExecutionConflict)
}

func TestGuestExecutionStoreRetainsPrivateDispatchData(t *testing.T) {
	ctx := context.Background()
	store := &localAPIStore{runsDir: t.TempDir()}
	want := controlplane.GuestExecution{
		ID: "exec-1", Topology: "lab", Node: "web", AgentID: "host-a",
		Status:  controlplane.GuestExecutionQueued,
		Request: controlplane.GuestExecutionRequest{Argv: []string{"env"}, Environment: map[string]string{"TOKEN": "secret"}},
	}
	require.NoError(t, store.SaveGuestExecution(ctx, want))
	got, err := store.GetGuestExecution(ctx, want.ID)
	require.NoError(t, err)
	require.Equal(t, want.AgentID, got.AgentID)
	require.Equal(t, want.Request, got.Request)
}

func TestExpiredGuestExecutionDropsPrivatePayload(t *testing.T) {
	now := time.Now().UTC()
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestExecutionService(store, nil, func() time.Time { return now })
	want := controlplane.GuestExecution{ID: "expired", Version: 1, Status: controlplane.GuestExecutionCompleted, Request: controlplane.GuestExecutionRequest{Argv: []string{"secret"}}, Result: controlplane.GuestExecutionResult{Stdout: "secret"}, ExpiresAt: now.Add(-time.Second)}
	require.NoError(t, store.SaveGuestExecution(context.Background(), want))
	got, err := svc.Get(context.Background(), want.ID)
	require.NoError(t, err)
	require.Empty(t, got.Request.Argv)
	require.Empty(t, got.Result.Stdout)
}

func TestGuestExecutionCreatePublishesPrivateRequestSeparately(t *testing.T) {
	ctx := context.Background()
	want := controlplane.GuestExecutionRequest{Argv: []string{"env"}, Environment: map[string]string{"TOKEN": "secret"}}
	var published controlplane.AgentCommand
	svc := newGuestExecutionService(&localAPIStore{runsDir: t.TempDir()}, func(_ context.Context, _ string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		published = cmd
		return cmd, nil
	}, nil)

	execution, err := svc.Create(ctx, "lab", "web", "host-a", want)
	require.NoError(t, err)
	require.Equal(t, want, published.ExecutionRequest)
	require.Empty(t, published.Execution.Request)

	raw, err := json.Marshal(execution)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "TOKEN")
	require.NotContains(t, string(raw), "secret")
}
