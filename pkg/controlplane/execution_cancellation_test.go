package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExecutionCancellationCannotAuthorizeStart(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "task", Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: "digest", TargetBootID: "guest", State: GrantRevoked, ExpiresAt: time.Now().Add(-time.Hour)}
	stop, err := SignExecutionCancellation(key, g)
	require.NoError(t, err)
	require.NoError(t, VerifyExecutionCancellation(pub, stop))
	changed := stop
	changed.Grant.Epoch++
	require.Error(t, VerifyExecutionCancellation(pub, changed))
	_, err = SignExecutionGrant(key, g, "guest", time.Now())
	require.Error(t, err)
	start := g
	start.State = GrantActive
	start.ExpiresAt = time.Now().Add(time.Minute)
	forged := SignedExecutionGrant{Grant: start, BootID: "guest", IssuedAt: time.Now(), Signature: stop.Signature}
	require.Error(t, VerifyExecutionGrant(pub, forged, "guest", time.Now()), "stop signature cannot be reinterpreted as a start credential")
	changed.Grant = g
	changed.Grant.State = GrantActive
	_, err = SignExecutionCancellation(key, changed.Grant)
	require.Error(t, err)
}
