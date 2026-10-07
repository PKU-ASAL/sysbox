package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdoptionReceiptBindsProviderEvidence(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Operation: "adoption_apply", AgentID: "agent", SessionID: "session", ActionID: "adopt", CommandID: "command", Attempt: 1, Epoch: 1, Digest: "request-digest", Scope: ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}}
	raw := json.RawMessage(`{ "domain_uuid": "original" }`)
	receipt, err := SignAdoptionStopReceipt(key, g, "boot", "receipt", raw, time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyAdoptionStopReceipt(pub, receipt, raw))
	encoded, err := json.Marshal(struct {
		Receipt  SignedExecutionStopReceipt
		Evidence json.RawMessage
	}{receipt, raw})
	require.NoError(t, err)
	var decoded struct {
		Receipt  SignedExecutionStopReceipt
		Evidence json.RawMessage
	}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NoError(t, VerifyAdoptionStopReceipt(pub, decoded.Receipt, decoded.Evidence))
	require.Error(t, VerifyAdoptionStopReceipt(pub, receipt, json.RawMessage(`{"domain_uuid":"different"}`)))
	_, err = SignExecutionStopReceipt(key, g, "boot", "receipt", time.Now())
	require.Error(t, err, "generic quiescence cannot attest adoption evidence")
	_, err = SignAdoptionStopReceipt(key, g, "boot", "receipt", json.RawMessage(`null`), time.Now())
	require.Error(t, err)
}
