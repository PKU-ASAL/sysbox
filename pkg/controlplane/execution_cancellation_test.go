package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func TestRunExecutionCancellationBindsSource(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "task", Attempt: 1, Epoch: 2, ParentEpoch: 1, Operation: "guest_task", Digest: "digest", TargetBootID: "guest", State: GrantRevoked, ExpiresAt: time.Now()}
	proof, err := SignRunExecutionCancellation(key, g, "run", "lab")
	require.NoError(t, err)
	require.NoError(t, VerifyExecutionCancellation(pub, proof))
	changed := proof
	changed.RunID = "other"
	require.Error(t, VerifyExecutionCancellation(pub, changed))
	changed = proof
	changed.Topology = "other"
	require.Error(t, VerifyExecutionCancellation(pub, changed))
	_, err = SignRunExecutionCancellation(key, g, "../run", "lab")
	require.Error(t, err)
	g.ParentEpoch = 0
	_, err = SignRunExecutionCancellation(key, g, "run", "lab")
	require.Error(t, err)
}
