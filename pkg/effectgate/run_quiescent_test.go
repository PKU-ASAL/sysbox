package effectgate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func failedRunResult(t *testing.T, agent string, status controlplane.RunStatus) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(controlplane.Run{ID: "run", AgentID: agent, Op: "apply", Status: status, EndedAt: time.Now().UTC()})
	require.NoError(t, err)
	return raw
}

func TestCommitQuiescentRunExactReplayAfterSuccessor(t *testing.T) {
	gate, signed, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, signed, "effect")
	require.NoError(t, err)
	result := failedRunResult(t, signed.Grant.AgentID, controlplane.RunFailed)
	require.NoError(t, gate.CommitQuiescentRun(ctx, signed, "effect", result))
	// A lost commit acknowledgement must be safely retried from disk.
	reopened := *gate
	require.NoError(t, reopened.CommitQuiescentRun(ctx, signed, "effect", result))
	receipt, err := reopened.SealStop(ctx, signed.Grant, "effect", func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignExecutionStopReceipt(key, grant, gate.BootID, "receipt", time.Now())
	})
	require.NoError(t, err)
	require.NoError(t, reopened.ReportStop(ctx, signed.Grant, "effect", func(_ context.Context, got controlplane.SignedExecutionStopReceipt) error {
		require.Equal(t, receipt, got)
		return nil
	}))
	next := signed.Grant
	next.Epoch++
	successor, err := controlplane.SignExecutionGrant(key, next, signed.BootID, time.Now())
	require.NoError(t, err)
	_, err = reopened.ReserveRun(ctx, successor, "next")
	require.NoError(t, err)
	path, _, release, err := reopened.locked(ctx, signed.Grant.Scope.TopologyUID)
	require.NoError(t, err)
	release()
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	var formatted bytes.Buffer
	require.NoError(t, json.Indent(&formatted, result, "", "  "))
	require.NoError(t, reopened.CommitQuiescentRun(ctx, signed, "effect", formatted.Bytes()))
	changed := failedRunResult(t, signed.Grant.AgentID, controlplane.RunCancelled)
	require.ErrorIs(t, reopened.CommitQuiescentRun(ctx, signed, "effect", changed), ErrFenced)
	stale := extendedRunGrant(t, key, signed, time.Minute)
	require.ErrorIs(t, reopened.CommitQuiescentRun(ctx, stale, "effect", result), ErrFenced)
	observed, err := reopened.ReadRun(ctx, successor, "next")
	require.NoError(t, err)
	require.Equal(t, RunIntent, observed.State)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "historical replay must preserve receipt, acknowledgement and successor")
}

func TestCommitQuiescentRunReplayPreservesJSONEncoding(t *testing.T) {
	gate, signed, _ := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, signed, "effect")
	require.NoError(t, err)
	result := json.RawMessage(`{"id":"run","agent_id":"agent","op":"apply","status":"failed","ended_at":"2026-10-09T00:00:00Z","error":"script <input> & output"}`)
	require.NoError(t, gate.CommitQuiescentRun(ctx, signed, "effect", result))
	require.NoError(t, gate.CommitQuiescentRun(ctx, signed, "effect", result))
}

func TestCommitQuiescentRunExpiredHistoricalIntent(t *testing.T) {
	for _, status := range []controlplane.RunStatus{controlplane.RunFailed, controlplane.RunCancelled} {
		t.Run(string(status), func(t *testing.T) {
			gate, signed, key := runGateFixture(t)
			ctx := context.Background()
			// Seed the exact durable state left by admission before its expiry.
			signed.Grant.ExpiresAt = time.Now().Add(-time.Minute)
			signed, err := controlplane.SignExecutionGrant(key, signed.Grant, signed.BootID, time.Now().Add(-2*time.Minute))
			require.NoError(t, err)
			path, ledger, release, err := gate.locked(ctx, signed.Grant.Scope.TopologyUID)
			require.NoError(t, err)
			ledger.Outcomes[outcomeKey(signed.Grant, "effect")] = outcome{Grant: signed.Grant, SignedGrant: &signed, State: string(RunIntent)}
			require.NoError(t, save(path, ledger))
			release()
			gate.Authorize = func(context.Context, controlplane.ExecutionGrant) error {
				t.Fatal("quiescent commit must not request execution authority")
				return ErrFenced
			}
			result := failedRunResult(t, signed.Grant.AgentID, status)
			require.NoError(t, gate.CommitQuiescentRun(ctx, signed, "effect", result))
			observed, err := gate.ReadRun(ctx, signed, "effect")
			require.NoError(t, err)
			require.Equal(t, RunComplete, observed.State)
			require.JSONEq(t, string(result), string(observed.Result))
		})
	}
}

func TestCommitQuiescentRunRejectsUntrustedOrInconsistentOutcome(t *testing.T) {
	for _, scenario := range []string{"forged", "stale", "success", "agent", "operation", "missing", "null", "no_end", "no_id"} {
		t.Run(scenario, func(t *testing.T) {
			gate, signed, key := runGateFixture(t)
			ctx := context.Background()
			if scenario != "missing" {
				_, err := gate.ReserveRun(ctx, signed, "effect")
				require.NoError(t, err)
			}
			proof := cloneRunEnvelope(signed)
			result := failedRunResult(t, signed.Grant.AgentID, controlplane.RunFailed)
			switch scenario {
			case "forged":
				proof.Signature[0] ^= 1
			case "stale":
				renewed := extendedRunGrant(t, key, signed, time.Minute)
				require.NoError(t, gate.RenewRun(ctx, signed, renewed, "effect"))
			case "success":
				result = failedRunResult(t, signed.Grant.AgentID, controlplane.RunDone)
			case "agent":
				result = failedRunResult(t, "different-agent", controlplane.RunFailed)
			case "operation", "no_end", "no_id":
				var run controlplane.Run
				require.NoError(t, json.Unmarshal(result, &run))
				switch scenario {
				case "operation":
					run.Op = "destroy"
				case "no_end":
					run.EndedAt = time.Time{}
				case "no_id":
					run.ID = ""
				}
				var err error
				result, err = json.Marshal(run)
				require.NoError(t, err)
			case "null":
				result = json.RawMessage(`null`)
			}
			require.Error(t, gate.CommitQuiescentRun(ctx, proof, "effect", result))
			observed, err := gate.ReadRun(ctx, signed, "effect")
			if scenario == "missing" {
				require.ErrorIs(t, err, ErrNotFound)
			} else {
				require.NoError(t, err)
				require.Equal(t, RunIntent, observed.State)
			}
		})
	}
}
