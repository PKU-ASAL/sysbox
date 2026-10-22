package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRebootedTaskReceiptBindsBothBootsAndUnknownResult(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "task", Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: "digest", TargetBootID: "old", State: GrantRevoked}
	receipt, err := SignRebootedTaskStopReceipt(key, g, "host", "new", "receipt", time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyExecutionStopReceipt(pub, receipt))
	require.Equal(t, "unknown", receipt.GuestResult.ResultClass)
	changed := receipt
	changed.ObservedGuestBootID = "another"
	require.Error(t, VerifyExecutionStopReceipt(pub, changed))
	_, err = SignRebootedTaskStopReceipt(key, g, "host", "old", "receipt", time.Now())
	require.Error(t, err)
	_, err = SignRebootedTaskStopReceipt(key, g, "host", "", "receipt", time.Now())
	require.Error(t, err)
}

func TestRebootedFileReceiptProvesStopWithoutInventingCommit(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "file", Attempt: 1, Epoch: 1, Operation: "guest_file_install", Digest: "digest", TargetBootID: "old", State: GrantRevoked}
	receipt, err := SignRebootedFileStopReceipt(key, g, "host", "new", "receipt", time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyExecutionStopReceipt(pub, receipt))
	require.Equal(t, "unknown", receipt.FileResult.Outcome)
	require.True(t, receipt.FileResult.Quiescent)
	require.Empty(t, receipt.FileResult.SHA256)
	changed := receipt
	changed.ObservedGuestBootID = "old"
	require.Error(t, VerifyExecutionStopReceipt(pub, changed))
	result := *receipt.FileResult
	result.Outcome = "committed"
	changed = receipt
	changed.FileResult = &result
	_, err = signStopReceipt(key, changed)
	require.Error(t, err)
	_, err = SignExecutionFileStopReceipt(key, g, "host", "old", g.ActionID, g.Digest, *receipt.FileResult, "no-reboot", time.Now())
	require.Error(t, err)
}
