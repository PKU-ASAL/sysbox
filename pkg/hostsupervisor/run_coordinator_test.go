package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func runFixture(t *testing.T) (*RunCoordinator, RunRequest, controlplane.SignedExecutionGrant) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	req := RunRequest{RunID: "run", Topology: "lab", Revision: "rev", ActionID: "action", ResourceUID: "resource", Generation: 1, Action: "apply", ExpectedSerial: 1}
	d, e := req.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "lab", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: d, State: controlplane.GrantActive, TargetBootID: "host", ExpiresAt: time.Now().Add(time.Minute)}
	s, e := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, e)
	_ = pub
	return &RunCoordinator{Root: t.TempDir(), IssuerKey: pub, HostBootID: "host"}, req, s
}
func TestRunCoordinatorPersistsIntentAndNeverReplaysLostEffect(t *testing.T) {
	c, req, g := runFixture(t)
	calls := 0
	effect := func(context.Context) (json.RawMessage, error) { calls++; return nil, errors.New("reply lost") }
	_, err := c.Start(context.Background(), req, g, effect)
	require.ErrorIs(t, err, ErrRunUnknown)
	again, err := c.Observe(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "intent", again.Phase)
	require.Equal(t, 1, calls)
	_, err = c.Start(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { calls++; return json.RawMessage(`{"ok":true}`), nil })
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
