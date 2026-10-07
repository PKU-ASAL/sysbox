package controlplane

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSSHAccessDigestCanonicalizesKeyOrder(t *testing.T) {
	a := SSHAccessRequest{ID: "access", Topology: "lab", Node: "web", AuthorizedKeys: []string{"ssh-ed25519 AAAA two", "ssh-ed25519 AAAA one"}}
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
