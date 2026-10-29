package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSSHStopReceiptBindsResult(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "operation", Attempt: 1, Epoch: 1, Operation: "ssh_access_apply", Digest: "request"}
	raw := json.RawMessage(`{"access_id":"admin","sha256":"hash"}`)
	receipt, err := SignSSHAccessStopReceipt(key, g, "boot", "receipt", raw, time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifySSHAccessStopReceipt(pub, receipt, raw))
	require.Error(t, VerifySSHAccessStopReceipt(pub, receipt, json.RawMessage(`{"access_id":"other","sha256":"hash"}`)))
}
