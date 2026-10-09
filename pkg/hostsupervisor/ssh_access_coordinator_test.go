package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"os"
	"strings"
	"testing"
	"time"
)

func sshFixture(t *testing.T) (*SSHAccessCoordinator, controlplane.SSHAccessRequest, controlplane.SignedExecutionGrant) {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	public, e := ssh.NewPublicKey(pub)
	require.NoError(t, e)
	req := controlplane.SSHAccessRequest{ID: "access", AccessID: "admin", Topology: "lab", Node: "web", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))}}
	d, e := req.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner-uid", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command", Attempt: 1, Epoch: 1, Operation: "ssh_access_apply", Digest: d, TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
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
	req.AuthorizedKeys = []string{req.AuthorizedKeys[0] + " changed"}
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.Error(t, err)
	_ = c
}

func TestSSHAccessServerRejectsRequestTargetMismatchBeforeEffect(t *testing.T) {
	for _, field := range []string{"topology", "node", "uid", "resource", "generation"} {
		t.Run(field, func(t *testing.T) {
			c, req, grant := sshFixture(t)
			target := TargetRef{Topology: req.Topology, Node: req.Node, Scope: grant.Grant.Scope}
			switch field {
			case "topology":
				target.Topology = "other"
			case "node":
				target.Node = "other"
			case "uid":
				target.Scope.TopologyUID = "other"
			case "resource":
				target.Scope.ResourceUID = "other"
			case "generation":
				target.Scope.Generation++
			}
			calls := 0
			server := &Server{SSH: c, SSHApply: func(context.Context, controlplane.SSHAccessRequest, controlplane.SignedExecutionGrant) (json.RawMessage, error) {
				calls++
				return json.RawMessage(`{}`), nil
			}}
			response := server.Handle(context.Background(), Request{Version: 1, Operation: "ssh-apply", Target: target, SSHRequest: &req, Grant: &grant})
			require.Contains(t, response.Error, "target mismatch")
			require.Zero(t, calls)
		})
	}
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
