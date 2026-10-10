//go:build e2e

package docker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestDockerProductionSSHAccessE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("current static SYSBOX_GUESTTASK_HELPER required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicPath := filepath.Join(t.TempDir(), "grant.pub")
	require.NoError(t, os.WriteFile(publicPath, pub, 0600))
	identity := substrate.OperationIdentity{ActionID: "fixture", OwnerUID: "ssh-provider-" + rand.Text(), ResourceUID: "node", Generation: 2}
	identity.TopologyUID = identity.OwnerUID
	valid := substrate.WithOperationIdentity(ctx, identity)
	created, err := s.cli.ContainerCreate(ctx, &container.Config{Image: "alpine:3.22", Cmd: []string{"sleep", "120"}, Labels: operationLabels(identity)},
		&container.HostConfig{NetworkMode: "none", Binds: []string{helper + ":/usr/local/bin/sysbox-guest-task:ro", publicPath + ":/etc/sysbox/grant-public-key:ro"}}, nil, nil, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.cli.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true})
	})
	h := substrate.NodeHandle{ID: created.ID, Generation: identity.Generation}
	require.NoError(t, s.StartNode(valid, h))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	access, ok := conn.(substrate.ConnectionSSHAccess)
	require.True(t, ok)
	boot, err := conn.(interface {
		GuestFileBootID(context.Context) (string, error)
	}).GuestFileBootID(valid)
	require.NoError(t, err)
	sshKey, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	mutation := substrate.SSHAccessMutation{ID: "create", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshKey)))}}}
	mutate := func(req substrate.SSHAccessMutation, epoch uint64) substrate.SSHAccessResult {
		t.Helper()
		digest, err := req.Digest()
		require.NoError(t, err)
		operation := "guest_ssh_access_apply"
		if req.Delete {
			operation = "guest_ssh_access_delete"
		}
		grant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{TargetBootID: boot,
			Scope:   controlplane.ExecutionScope{TopologyUID: identity.TopologyUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation},
			AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: req.ID, Attempt: 1, Epoch: epoch, Operation: operation,
			Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, boot, time.Now())
		require.NoError(t, err)
		result, err := access.MutateSSHAccess(transport.WithTaskGrant(valid, req.ID, grant), req)
		require.NoError(t, err)
		return result
	}
	first := mutate(mutation, 1)
	query := substrate.SSHAccessQuery{NodeID: h.ID, User: "ubuntu", AccessID: "admin", TopologyUID: identity.TopologyUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation}
	found, err := access.DiscoverSSHAccess(valid, query)
	require.NoError(t, err)
	require.Equal(t, first, *found.Current)
	stale := identity
	stale.Generation--
	_, err = access.DiscoverSSHAccess(substrate.WithOperationIdentity(ctx, stale), query)
	require.Error(t, err, "real provider must reject stale ownership proof before helper call")
	cold, err := s.Connection(h, nil)
	require.NoError(t, err)
	observed, err := cold.(substrate.ConnectionSSHAccess).ObserveSSHAccess(valid, first.Handle)
	require.NoError(t, err)
	require.Equal(t, first, observed)
	mutation = substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	deleted := mutate(mutation, 2)
	require.False(t, deleted.Present)
	found, err = access.DiscoverSSHAccess(valid, query)
	require.NoError(t, err)
	require.Equal(t, "absent", found.Status)
	require.Equal(t, deleted, *found.Current)
	require.NoError(t, s.DestroyNode(valid, h))
	_, err = s.cli.ContainerInspect(ctx, h.ID)
	require.True(t, errdefs.IsNotFound(err), "normal provider Destroy must remove the exact fixture")
	t.Log("production Docker connection: signed SSH mutation, discovery, cold observation, tombstone, stale proof rejection and normal Destroy passed")
}
