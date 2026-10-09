package controlplane

import (
	"crypto/ed25519"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
)

func sshAccessTestKey(t *testing.T) string {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestSSHAccessDigestCanonicalizesKeyOrder(t *testing.T) {
	key := sshAccessTestKey(t)
	a := SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{key + " two", key + " one"}}
	d1, err := a.Digest()
	require.NoError(t, err)
	a.AuthorizedKeys[0], a.AuthorizedKeys[1] = a.AuthorizedKeys[1], a.AuthorizedKeys[0]
	d2, err := a.Digest()
	require.NoError(t, err)
	require.Equal(t, d1, d2)
}
func TestSSHAccessRejectsPrivateKeysAndNewlineInjection(t *testing.T) {
	bad := SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{"ssh-private-key secret"}}
	require.Error(t, bad.Validate())
	bad.AuthorizedKeys = []string{"ssh-ed25519 AAAA\ncommand=rm"}
	require.Error(t, bad.Validate())
}

func TestSSHAccessCommandRejectsMismatchedTarget(t *testing.T) {
	req := SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{sshAccessTestKey(t)}}
	command := AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner-uid", ResourceUID: "resource", Generation: 1, ActionID: req.ID, Operation: NodeOperation{Topology: "lab", Node: "web", SSHAccess: &req}}
	_, err := FromSSHAccessCommand(command)
	require.NoError(t, err)
	command.Operation.Node = "other"
	_, err = FromSSHAccessCommand(command)
	require.Error(t, err)
	command.Operation.Node = "web"
	command.Operation.Topology = "other"
	_, err = FromSSHAccessCommand(command)
	require.Error(t, err)
}

func TestSSHAccessRejectsMalformedWireKeysAndOptions(t *testing.T) {
	for _, key := range []string{"ssh-ed25519 AAAA key", "unknown data", `command="touch /tmp/file" ` + sshAccessTestKey(t), "ssh-rsa " + strings.Fields(sshAccessTestKey(t))[1], sshAccessTestKey(t) + " \xff"} {
		req := SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{key}}
		require.Error(t, req.Validate(), key)
	}
}

func TestSSHAccessContentCanonicalizesOrderWithoutMutatingRequest(t *testing.T) {
	key := sshAccessTestKey(t)
	req := SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{key + " z", key + " a"}}
	before := append([]string(nil), req.AuthorizedKeys...)
	content, err := req.AuthorizedKeysContent()
	require.NoError(t, err)
	require.Equal(t, before, req.AuthorizedKeys)
	req.AuthorizedKeys[0], req.AuthorizedKeys[1] = req.AuthorizedKeys[1], req.AuthorizedKeys[0]
	again, err := req.AuthorizedKeysContent()
	require.NoError(t, err)
	require.Equal(t, content, again)
}
