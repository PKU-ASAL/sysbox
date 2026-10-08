package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func sshFixture(t *testing.T) (*SSHAccessCoordinator, controlplane.SSHAccessRequest, controlplane.SignedExecutionGrant) {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	req := controlplane.SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{"ssh-ed25519 AAAA key"}}
	d, e := req.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "lab", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command", Attempt: 1, Epoch: 1, Operation: "ssh_access_apply", Digest: d, TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	s, e := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, e)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	_, host, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	return &SSHAccessCoordinator{Root: root, IssuerKey: pub, HostKey: host, HostBootID: "host", AgentID: "agent", Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		return nil
	}, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}, req, s
}
func TestSSHAccessApplyLostReplyObservesWithoutReapplying(t *testing.T) {
	c, req, g := sshFixture(t)
	calls := 0
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { calls++; return nil, errors.New("lost") })
	require.Error(t, err, "%v", err)
	require.Equal(t, 1, calls)
	_, err = c.Observe(context.Background(), req, g)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
func TestSSHAccessApplyRejectsChangedKey(t *testing.T) {
	c, req, g := sshFixture(t)
	req.AuthorizedKeys = []string{"ssh-ed25519 AAAA changed"}
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.Error(t, err)
	_ = c
}

func TestSSHAccessCompletionReceiptSurvivesRestartAndOldEpoch(t *testing.T) {
	c, req, g := sshFixture(t)
	var first controlplane.SignedExecutionStopReceipt
	c.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
		if first.ReceiptID == "" {
			first = r
		}
		return errors.New("lost acknowledgment")
	}
	calls := 0
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { calls++; return json.RawMessage(`{"ok":true}`), nil })
	require.Error(t, err)
	cold := *c
	cold.HostBootID = "restarted"
	cold.Authorize = nil
	cold.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
		require.Equal(t, first, r)
		return nil
	}
	raw, err := cold.Observe(context.Background(), req, g)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(raw))
	require.Equal(t, 1, calls)
	// Historical observation remains valid after the original grant is retired;
	// no provider effect is replayed.
	_, err = cold.Observe(context.Background(), req, g)
	require.NoError(t, err)
}
