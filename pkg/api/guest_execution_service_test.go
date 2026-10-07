package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
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
	ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "guest", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1})
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestExecutionServiceFromStore(store, nil, nil)
	exec, err := svc.Create(ctx, "lab", "web", "host-a", controlplane.GuestExecutionRequest{Argv: []string{"sh", "-c", "exit 7"}, TimeoutSeconds: 30})
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionQueued, exec.Status)

	exec, err = svc.MarkRunning(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionRunning, exec.Status)

	lease, ok, err := store.AcquireAgentCommandLease(ctx, exec.AgentID, exec.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	report := controlplane.GuestExecutionCompletion{Result: controlplane.GuestExecutionResult{ExitCode: 7, Stderr: "YmFk", Encoding: "base64"}, ResultClass: controlplane.GuestExecutionResultClassExit, ActionID: lease.ActionID, ResourceUID: lease.ResourceUID, Generation: lease.Generation, LeaseEpoch: lease.LeaseEpoch, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt}
	exec, err = store.CompleteGuestExecution(ctx, exec.AgentID, exec.ID, report)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCompleted, exec.Status)
	require.Equal(t, 7, exec.Result.ExitCode)
	require.Equal(t, controlplane.GuestExecutionResultClassExit, exec.ResultClass)
	report.Result.ExitCode = 0
	_, err = store.CompleteGuestExecution(ctx, exec.AgentID, exec.ID, report)
	require.ErrorIs(t, err, persistence.ErrConflict)

	raw, err := json.Marshal(exec)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "host-a")
	require.NotContains(t, string(raw), "environment")
}

func TestGuestExecutionCancelIsExplicitTerminalTransition(t *testing.T) {
	ctx := context.Background()
	var cancelTarget string
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestExecutionService(store, func(_ context.Context, _ string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
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
	_, err = store.CompleteGuestExecution(ctx, exec.AgentID, exec.ID, controlplane.GuestExecutionCompletion{})
	require.ErrorIs(t, err, persistence.ErrConflict)
}

func TestGuestExecutionStoreRetainsPrivateDispatchData(t *testing.T) {
	ctx := context.Background()
	store := &localAPIStore{runsDir: t.TempDir()}
	want := controlplane.GuestExecution{
		ID: "exec-1", Version: 1, Topology: "lab", Node: "web", AgentID: "host-a",
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

func TestGuestExecutionOutboxCommitSurvivesNotificationFailure(t *testing.T) {
	ctx := context.Background()
	store := &localAPIStore{runsDir: t.TempDir()}
	var queued controlplane.AgentCommand
	svc := newGuestExecutionServiceWithOutbox(store, func(_ context.Context, _ string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		return cmd, errors.New("agent disconnected")
	}, func(ctx context.Context, execution controlplane.GuestExecution, cmd controlplane.AgentCommand) (controlplane.GuestExecution, error) {
		queued = cmd
		execution.CommandID = cmd.ID
		if err := store.SaveGuestExecution(ctx, execution); err != nil {
			return controlplane.GuestExecution{}, err
		}
		if err := store.SaveAgentCommand(ctx, cmd); err != nil {
			return controlplane.GuestExecution{}, err
		}
		return execution, nil
	}, nil)
	execution, err := svc.Create(ctx, "lab", "web", "host-a", controlplane.GuestExecutionRequest{Argv: []string{"true"}})
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionQueued, execution.Status)
	require.Equal(t, queued.ID, execution.CommandID)
	stored, err := store.GetGuestExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, queued.ID, stored.CommandID)
	commands, err := store.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}

func TestGuestExecutionUsesStoreOutboxWhenAvailable(t *testing.T) {
	ctx := context.Background()
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestExecutionServiceWithOutbox(store, func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		return controlplane.AgentCommand{}, errors.New("notification unavailable")
	}, func(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) (controlplane.GuestExecution, error) {
		return execution, store.CreateGuestExecutionDispatch(ctx, execution, command)
	}, nil)
	execution, err := svc.Create(ctx, "lab", "web", "agent", controlplane.GuestExecutionRequest{Argv: []string{"true"}})
	require.NoError(t, err)
	require.NotEmpty(t, execution.CommandID)
	commands, err := store.ListAgentCommands(ctx, "agent")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}
