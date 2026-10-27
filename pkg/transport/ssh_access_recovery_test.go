package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type sshLostReply struct {
	engine    *guesttask.Engine
	mutations int
}

func (c *sshLostReply) taskCall(ctx context.Context, call guesttask.Call) (guesttask.Response, error) {
	response := c.engine.Handle(ctx, call)
	if response.Error != "" {
		return guesttask.Response{}, fmt.Errorf("guest: %s", response.Error)
	}
	if call.Operation == "ssh-access" {
		c.mutations++
		return guesttask.Response{}, io.ErrUnexpectedEOF
	}
	return response, nil
}

func TestSSHAccessLostReplyRecoversOriginalResultWithoutMutation(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshKey, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	caller := &sshLostReply{engine: &guesttask.Engine{Root: root, PublicKey: pub}}
	boot, err := guesttask.BootID()
	require.NoError(t, err)
	identity := executionIdentity{nodeID: "node", generation: 1}
	req := substrate.SSHAccessMutation{ID: "create", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshKey)))}}}
	for i, deleting := range []bool{false, true} {
		t.Run(fmt.Sprint(deleting), func(t *testing.T) {
			operation := "guest_ssh_access_apply"
			if deleting {
				req = substrate.SSHAccessMutation{ID: "delete", ExpectedRevision: 1, Delete: true, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
				operation = "guest_ssh_access_delete"
			}
			digest, err := req.Digest()
			require.NoError(t, err)
			grant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{
				Scope:        controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node-uid", Generation: 1},
				TargetBootID: boot, AgentID: "agent", SessionID: "session", CommandID: req.ID, ActionID: req.ID,
				Attempt: 1, Epoch: uint64(i + 1), Digest: digest, Operation: operation, State: controlplane.GrantActive,
				ExpiresAt: time.Now().Add(time.Minute),
			}, boot, time.Now())
			require.NoError(t, err)
			unknown, err := mutateSSHAccess(WithTaskGrant(context.Background(), req.ID, grant), caller, identity, req)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.Equal(t, substrate.SSHAccessHandle{NodeID: "node", ID: req.ID, Digest: digest,
				User: "ubuntu", AccessID: "admin", Revision: uint64(i + 1), BootID: boot,
				Epoch: uint64(i + 1), TopologyUID: "owner", ResourceUID: "node-uid", Generation: 1}, unknown.Handle)
			caller.engine = &guesttask.Engine{Root: root, PublicKey: pub}
			recovered, err := observeSSHAccess(context.Background(), caller, identity, unknown.Handle)
			require.NoError(t, err)
			require.Equal(t, !deleting, recovered.Present)
			require.Equal(t, unknown.Handle, recovered.Handle)
			require.Equal(t, i+1, caller.mutations, "recovery must not submit another mutation")
			query := substrate.SSHAccessQuery{NodeID: "node", User: "ubuntu", AccessID: "admin", TopologyUID: "owner", ResourceUID: "node-uid", Generation: 1}
			found, err := discoverSSHAccess(context.Background(), caller, identity, query)
			require.NoError(t, err)
			require.Equal(t, recovered, *found.Current)
			require.Equal(t, req.Config, *found.Config)
			require.Equal(t, query, found.Query)
			require.Equal(t, i+1, caller.mutations)
		})
	}
}
