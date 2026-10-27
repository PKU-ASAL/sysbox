package guesttask

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestSSHDiscoveryIsReadOnlyAndPreservesTombstoneRevision(t *testing.T) {
	e, key, req := sshAccessFixture(t)
	ctx := context.Background()
	query := substrate.SSHAccessQuery{User: "ubuntu", AccessID: "admin", TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	missing, err := e.DiscoverSSHAccess(ctx, query)
	require.NoError(t, err)
	require.Equal(t, "absent", missing.Status)
	require.Nil(t, missing.Current)
	files, err := os.ReadDir(e.Root)
	require.NoError(t, err)
	require.Empty(t, files, "discovery cannot create locks or authorize writes")
	first, err := e.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 1))
	require.NoError(t, err)
	cold := &Engine{Root: e.Root, PublicKey: e.PublicKey}
	found, err := cold.DiscoverSSHAccess(ctx, query)
	require.NoError(t, err)
	require.Equal(t, "present", found.Status)
	require.Equal(t, first, *found.Current)
	require.Equal(t, req.Config, *found.Config)
	require.NotEmpty(t, found.BootID)
	remove := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	deleted, err := e.MutateSSHAccess(ctx, remove, sshAccessGrant(t, key, remove, 2))
	require.NoError(t, err)
	found, err = cold.DiscoverSSHAccess(ctx, query)
	require.NoError(t, err)
	require.Equal(t, "absent", found.Status)
	require.Equal(t, deleted, *found.Current, "absence must retain the last committed revision")
}

func TestSSHDiscoveryRejectsForeignIdentityAndCorruption(t *testing.T) {
	e, key, req := sshAccessFixture(t)
	ctx := context.Background()
	_, err := e.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 1))
	require.NoError(t, err)
	query := substrate.SSHAccessQuery{User: "ubuntu", AccessID: "admin", TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	for _, field := range []string{"owner", "resource", "generation", "invalid"} {
		q := query
		switch field {
		case "owner":
			q.TopologyUID = "other"
		case "resource":
			q.ResourceUID = "other"
		case "generation":
			q.Generation++
		case "invalid":
			q.User = "../root"
		}
		found, err := e.DiscoverSSHAccess(ctx, q)
		require.Error(t, err)
		require.Equal(t, "unknown", found.Status)
		require.Nil(t, found.Current)
	}
	path := filepath.Join(e.sshAccessStore().Root, sshAccessName("ubuntu", "admin"))
	require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0600))
	found, err := e.DiscoverSSHAccess(ctx, query)
	require.Error(t, err)
	require.Equal(t, "unknown", found.Status)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = e.DiscoverSSHAccess(ctx, query)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSSHDiscoveryDoesNotTreatUntrustedPathsAsAbsence(t *testing.T) {
	for _, scenario := range []string{"record-symlink", "store-symlink", "record-directory"} {
		t.Run(scenario, func(t *testing.T) {
			e, _, _ := sshAccessFixture(t)
			store := e.sshAccessStore().Root
			if scenario == "store-symlink" {
				require.NoError(t, os.Symlink(filepath.Join(e.Root, "missing"), store))
			} else {
				require.NoError(t, os.Mkdir(store, 0700))
				path := filepath.Join(store, sshAccessName("ubuntu", "admin"))
				if scenario == "record-symlink" {
					require.NoError(t, os.Symlink(filepath.Join(e.Root, "missing"), path))
				} else {
					require.NoError(t, os.Mkdir(path, 0700))
				}
			}
			found, err := e.DiscoverSSHAccess(context.Background(), substrate.SSHAccessQuery{User: "ubuntu", AccessID: "admin", TopologyUID: "owner", ResourceUID: "node", Generation: 1})
			require.Error(t, err)
			require.Equal(t, "unknown", found.Status)
			require.Nil(t, found.Current)
		})
	}
}
