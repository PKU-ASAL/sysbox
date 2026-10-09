package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSignedGrantRejectsTamperWrongBootAndExpiry(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	grant := ExecutionGrant{TargetBootID: "boot", Scope: ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", Epoch: 1, Attempt: 1, ActionID: "action", CommandID: "command", Operation: "guest_task", Digest: "digest", State: GrantActive, ExpiresAt: now.Add(time.Minute)}
	signed, err := SignExecutionGrant(key, grant, "boot", now)
	require.NoError(t, err)
	missingBoot := grant
	missingBoot.TargetBootID = ""
	_, err = SignExecutionGrant(key, missingBoot, "boot", now)
	require.Error(t, err)
	require.NoError(t, VerifyExecutionGrant(pub, signed, "boot", now))
	require.Error(t, VerifyExecutionGrant(pub, signed, "other-boot", now))
	require.Error(t, VerifyExecutionGrant(pub, signed, "boot", now.Add(time.Minute)))
	signed.Grant.Scope.ResourceUID = "different"
	require.Error(t, VerifyExecutionGrant(pub, signed, "boot", now))
}

func TestStopReceiptRequiresSeparateHostSignature(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, worker, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{TargetBootID: "guest-boot", Scope: ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: "digest", ExpiresAt: time.Now().Add(time.Minute), State: GrantRevoked}
	r, err := SignExecutionTaskStopReceipt(key, g, "host-boot", "guest-boot", g.ActionID, g.Digest, GuestTaskResult{Result: GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "receipt", time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyExecutionStopReceipt(pub, r))
	r.GuestResult.Result.ExitCode = 9
	require.Error(t, VerifyExecutionStopReceipt(pub, r), "host result is covered by the signature")
	_, err = SignExecutionTaskStopReceipt(key, g, "host-boot", "wrong-guest-boot", g.ActionID, g.Digest, GuestTaskResult{Result: GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "receipt", time.Now())
	require.Error(t, err)
	_, err = SignExecutionTaskStopReceipt(key, g, "host-boot", "guest-boot", "wrong-task", g.Digest, GuestTaskResult{Result: GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "receipt", time.Now())
	require.Error(t, err)
	r.Grant.Epoch++
	require.Error(t, VerifyExecutionStopReceipt(pub, r))
	r, err = SignExecutionTaskStopReceipt(worker, g, "host-boot", "guest-boot", g.ActionID, g.Digest, GuestTaskResult{Result: GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "receipt", time.Now())
	require.NoError(t, err)
	require.Error(t, VerifyExecutionStopReceipt(pub, r))
}

func TestGrantBudgetAllowsBoundedSkewWithoutExtendingAuthority(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	g := ExecutionGrant{TargetBootID: "boot", Scope: ExecutionScope{TopologyUID: "t"}, AgentID: "a", SessionID: "s", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "exec", Digest: "digest", State: GrantActive, ExpiresAt: now.Add(time.Minute)}
	s, err := SignExecutionGrant(key, g, "boot", now)
	require.NoError(t, err)
	guestNow := now.Add(-500 * time.Millisecond)
	require.NoError(t, VerifyExecutionGrant(pub, s, "boot", guestNow))
	budget, err := ExecutionGrantBudget(s, guestNow)
	require.NoError(t, err)
	require.LessOrEqual(t, budget, time.Minute)
	require.Error(t, VerifyExecutionGrant(pub, s, "boot", now.Add(-2*time.Second)))
	_, err = ExecutionGrantBudget(s, g.ExpiresAt.Add(-500*time.Millisecond))
	require.Error(t, err)
}
