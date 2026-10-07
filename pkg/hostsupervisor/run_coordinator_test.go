package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func runFixture(t *testing.T) (*RunCoordinator, RunRequest, controlplane.SignedExecutionGrant) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "uid", Revision: "rev", ActionID: "action", ResourceUID: "resource", Generation: 1, Action: "apply", Target: "sysbox_node.web", ExpectedSerial: 1}
	d, e := req.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "uid", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: d, State: controlplane.GrantActive, TargetBootID: "host", ExpiresAt: time.Now().Add(time.Minute)}
	s, e := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, e)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	return &RunCoordinator{Root: root, IssuerKey: pub, HostBootID: "host", AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}, req, s
}
func TestRunCoordinatorPersistsIntentAndNeverReplaysLostEffect(t *testing.T) {
	c, req, g := runFixture(t)
	calls := 0
	effect := func(context.Context) (json.RawMessage, error) { calls++; return nil, errors.New("reply lost") }
	_, err := c.Start(context.Background(), req, g, effect)
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	_, err = c.Observe(context.Background(), req, g)
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
	_, err = c.Start(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { calls++; return json.RawMessage(`{"ok":true}`), nil })
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
}

func TestRunCoordinatorRequiresSignatureAndOnlineAuthority(t *testing.T) {
	c, req, g := runFixture(t)
	calls := 0
	effect := func(context.Context) (json.RawMessage, error) { calls++; return json.RawMessage(`{}`), nil }
	bad := g
	bad.Signature = make([]byte, ed25519.SignatureSize)
	_, err := c.Start(context.Background(), req, bad, effect)
	require.Error(t, err)
	c.Authorize = nil
	_, err = c.Start(context.Background(), req, g, effect)
	require.Error(t, err)
	require.Zero(t, calls)
}

func TestRunCoordinatorHistoricalReadSurvivesHostRestart(t *testing.T) {
	c, req, g := runFixture(t)
	_, err := c.Start(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"observed":true}`), nil })
	require.NoError(t, err)
	c.HostBootID = "new-boot"
	c.Authorize = nil
	raw, err := c.Observe(context.Background(), req, g)
	require.NoError(t, err)
	require.JSONEq(t, `{"observed":true}`, string(raw))
}
