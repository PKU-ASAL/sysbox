package persistence

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/stretchr/testify/require"
)

func hostGuestFixture(t *testing.T) ([]byte, CommandRecord, controlplane.SignedExecutionStopReceipt) {
	t.Helper()
	now := time.Now().UTC()
	x := controlplane.GuestExecution{ID: "execution", Version: 1, AgentID: "agent", CommandID: "command", Topology: "lab", Node: "node", Status: "queued", CreatedAt: now.Add(-time.Minute), Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
	cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", TopologyUID: "topology", ResourceUID: "resource", Generation: 1, ActionID: x.ID, Execution: &x, ExecutionRequest: x.Request}
	task, err := guesttask.FromExecutionCommand(cmd)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	raw, err := EncodeGuestExecutionDispatchRecord(x, cmd)
	require.NoError(t, err)
	cmd.Execution = nil
	cmd.ExecutionRequest = controlplane.GuestExecutionRequest{}
	cmd.Status = "cancelled"
	cmd.LeaseUntil = now.Add(-time.Hour)
	receipt := controlplane.SignedExecutionStopReceipt{Grant: controlplane.ExecutionGrant{AgentID: x.AgentID, CommandID: x.CommandID, ActionID: x.ID, Operation: "guest_task", TargetBootID: "guest", Attempt: 1, Epoch: 1, Digest: digest, Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}}, GuestBootID: "guest", TaskID: x.ID, TaskDigest: digest, StoppedAt: now, Quiescence: "confirmed", GuestResult: &controlplane.GuestTaskResult{ExitCodeKnown: true, ResultClass: "exit", Result: controlplane.GuestExecutionResult{ExitCode: 7, Encoding: "base64", Stdout: "b3V0"}}}
	return raw, CommandRecord{Command: cmd, Retired: true}, receipt
}

func TestHostGuestCompletionSurvivesRetirementAndRedaction(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		ctx := context.Background()
		repo := open()
		raw, cmd, receipt := hostGuestFixture(t)
		next, err := CompleteGuestRecord(raw, &cmd, "agent", "execution", GuestCompletion{HostExecution: &receipt}, time.Now())
		require.NoError(t, err)
		require.NoError(t, repo.records.update(ctx, guestExecutionKind, "execution", func([]byte) ([]byte, error) { return next, nil }))
		got, err := repo.GetGuestExecution(ctx, "execution")
		require.NoError(t, err)
		require.Equal(t, "completed", got.Status)
		require.Equal(t, 7, got.Result.ExitCode)
		got.Request = controlplane.GuestExecutionRequest{}
		got.Result.Stdout = ""
		ok, err := repo.CompareAndSwapGuestExecution(ctx, *got, got.Version)
		require.NoError(t, err)
		require.True(t, ok)
		redacted, err := repo.records.get(ctx, guestExecutionKind, "execution")
		require.NoError(t, err)
		replay, err := CompleteGuestRecord(redacted, &cmd, "agent", "execution", GuestCompletion{HostExecution: &receipt}, time.Now())
		require.NoError(t, err)
		require.Equal(t, redacted, replay)
		changed := receipt
		changed.ReceiptID = "different"
		_, err = CompleteGuestRecord(redacted, &cmd, "agent", "execution", GuestCompletion{HostExecution: &changed}, time.Now())
		require.ErrorIs(t, err, ErrConflict)
	})
}

func TestHostGuestCompletionRejectsUnboundEvidence(t *testing.T) {
	for _, kind := range []string{"digest", "generation", "node", "overflow", "unknown-class", "missing-result"} {
		t.Run(kind, func(t *testing.T) {
			raw, cmd, receipt := hostGuestFixture(t)
			switch kind {
			case "digest":
				receipt.Grant.Digest = "other"
				receipt.TaskDigest = "other"
			case "generation":
				receipt.Grant.Scope.Generation++
			case "node", "overflow":
				x, err := DecodeGuestExecution("execution", raw)
				require.NoError(t, err)
				if kind == "node" {
					x.Node = "other"
				} else {
					x.Version = math.MaxInt64
				}
				edited, err := encodeExecution(x)
				require.NoError(t, err)
				raw, err = preserveGuestDispatch(raw, edited)
				require.NoError(t, err)
			case "unknown-class":
				receipt.GuestResult.ResultClass = "invented"
			case "missing-result":
				receipt.GuestResult = nil
			}
			_, err := CompleteGuestRecord(raw, &cmd, "agent", "execution", GuestCompletion{HostExecution: &receipt}, time.Now())
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}
