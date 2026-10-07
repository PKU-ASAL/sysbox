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
	return &SSHAccessCoordinator{Root: root, IssuerKey: pub, HostBootID: "host", AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}, req, s
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
