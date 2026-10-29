package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunStopReceiptRejectsUnrelatedPayload(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "owner"}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_reset", Digest: "digest"}
	raw := json.RawMessage(`{"id":"run","op":"reset"}`)
	receipt, err := SignRunStopReceipt(key, g, "host", "receipt", raw, time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyRunStopReceipt(pub, receipt, raw))
	receipt.GuestResult = &GuestTaskResult{}
	receipt, err = signStopReceipt(key, receipt)
	require.NoError(t, err, "generic signature alone does not establish Run evidence shape")
	require.ErrorIs(t, VerifyRunStopReceipt(pub, receipt, raw), ErrInvalidExecutionGrant)
}
