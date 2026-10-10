package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func sshAccessFixture(t *testing.T) (*Engine, ed25519.PrivateKey, substrate.SSHAccessMutation) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	return &Engine{Root: root, PublicKey: pub}, key, substrate.SSHAccessMutation{
		ID: "create", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{sshAccessKey(t)}},
	}
}

func sshAccessKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	k, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

func sshAccessGrant(t *testing.T, key ed25519.PrivateKey, req substrate.SSHAccessMutation, epoch uint64) SignedGrant {
	t.Helper()
	digest, err := req.Digest()
	require.NoError(t, err)
	boot, err := BootID()
	require.NoError(t, err)
	op := "guest_ssh_access_apply"
	if req.Delete {
		op = "guest_ssh_access_delete"
	}
	g, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{
		Scope:        controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1},
		TargetBootID: boot, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: req.ID,
		Attempt: 1, Epoch: epoch, Operation: op, Digest: digest, State: controlplane.GrantActive,
		ExpiresAt: time.Now().Add(time.Minute),
	}, boot, time.Now())
	require.NoError(t, err)
	return g
}

func TestSSHAccessRotationRejectsOldDeleteAndPreservesOtherAccess(t *testing.T) {
	e, key, create := sshAccessFixture(t)
	ctx := context.Background()
	first, err := e.MutateSSHAccess(ctx, create, sshAccessGrant(t, key, create, 1))
	require.NoError(t, err)
	other := create
	other.ID, other.Config.AccessID = "other", "rescue"
	other.Config.AuthorizedKeys = []string{sshAccessKey(t)}
	_, err = e.MutateSSHAccess(ctx, other, sshAccessGrant(t, key, other, 2))
	require.NoError(t, err)
	rotate := create
	rotate.ID, rotate.ExpectedRevision = "rotate", 1
	rotate.Config.AuthorizedKeys = []string{sshAccessKey(t)}
	second, err := e.MutateSSHAccess(ctx, rotate, sshAccessGrant(t, key, rotate, 3))
	require.NoError(t, err)
	require.Equal(t, uint64(2), second.Handle.Revision)
	remove := substrate.SSHAccessMutation{ID: "remove", Delete: true, ExpectedRevision: 1,
		Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	_, err = e.MutateSSHAccess(ctx, remove, sshAccessGrant(t, key, remove, 4))
	require.ErrorIs(t, err, ErrConflict)
	keys, err := e.SSHAuthorizedKeys(ctx, "ubuntu", "127.0.0.1", 22)
	require.NoError(t, err)
	require.NotContains(t, string(keys), create.Config.AuthorizedKeys[0])
	require.Contains(t, string(keys), rotate.Config.AuthorizedKeys[0])
	require.Contains(t, string(keys), other.Config.AuthorizedKeys[0])
	remove.ExpectedRevision = 2
	deleted, err := e.MutateSSHAccess(ctx, remove, sshAccessGrant(t, key, remove, 4))
	require.NoError(t, err)
	require.Equal(t, uint64(3), deleted.Handle.Revision)
	require.False(t, deleted.Present)
	cold := &Engine{Root: e.Root, PublicKey: e.PublicKey}
	historical, err := cold.ObserveSSHAccess(ctx, first.Handle)
	require.NoError(t, err)
	require.Equal(t, first, historical)
	inspection, err := cold.InspectSSHAccess(ctx, first.Handle)
	require.NoError(t, err)
	require.Equal(t, "absent", inspection.Status)
	require.Equal(t, uint64(3), inspection.Revision)
	keys, err = cold.SSHAuthorizedKeys(ctx, "ubuntu", "127.0.0.1", 22)
	require.NoError(t, err)
	require.Equal(t, other.Config.AuthorizedKeys[0]+"\n", string(keys))
	_, err = cold.MutateSSHAccess(ctx, create, sshAccessGrant(t, key, create, 1))
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestSSHAccessConcurrentCASHasOneWinner(t *testing.T) {
	e, key, create := sshAccessFixture(t)
	ctx := context.Background()
	_, err := e.MutateSSHAccess(ctx, create, sshAccessGrant(t, key, create, 1))
	require.NoError(t, err)
	changes := []substrate.SSHAccessMutation{create, create}
	changes[0].ID, changes[1].ID = "rotate-a", "rotate-b"
	changes[0].ExpectedRevision, changes[1].ExpectedRevision = 1, 1
	grants := []SignedGrant{sshAccessGrant(t, key, changes[0], 2), sshAccessGrant(t, key, changes[1], 2)}
	var wg sync.WaitGroup
	errors := make([]error, 2)
	for i := range changes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cold := &Engine{Root: e.Root, PublicKey: e.PublicKey}
			_, errors[i] = cold.MutateSSHAccess(ctx, changes[i], grants[i])
		}(i)
	}
	wg.Wait()
	if errors[0] == nil {
		require.ErrorIs(t, errors[1], ErrConflict)
	} else {
		require.ErrorIs(t, errors[0], ErrConflict)
		require.NoError(t, errors[1])
	}
}

func TestSSHAccessEndpointAndRequestIdentity(t *testing.T) {
	e, key, req := sshAccessFixture(t)
	req.Config.BindIP, req.Config.Port = "127.0.0.1", 2222
	ctx := context.Background()
	g := sshAccessGrant(t, key, req, 1)
	first, err := e.MutateSSHAccess(ctx, req, g)
	require.NoError(t, err)
	retry, err := e.MutateSSHAccess(ctx, req, g)
	require.NoError(t, err)
	require.Equal(t, first, retry)
	for _, endpoint := range []struct {
		user, ip string
		port     int
	}{{"root", "127.0.0.1", 2222}, {"ubuntu", "127.0.0.2", 2222}, {"ubuntu", "127.0.0.1", 22}} {
		keys, err := e.SSHAuthorizedKeys(ctx, endpoint.user, endpoint.ip, endpoint.port)
		require.NoError(t, err)
		require.Empty(t, keys)
	}
	req.Config.AuthorizedKeys = []string{sshAccessKey(t)}
	_, err = e.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 1))
	require.ErrorIs(t, err, ErrConflict)
	keys, err := e.SSHAuthorizedKeys(ctx, "ubuntu", "127.0.0.1", 2222)
	require.NoError(t, err)
	require.NotContains(t, string(keys), req.Config.AuthorizedKeys[0])
}
