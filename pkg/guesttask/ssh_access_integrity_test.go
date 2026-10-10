package guesttask

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestSSHAccessCorruptionNeverAuthorizesKeysOrDeletesRecord(t *testing.T) {
	for _, corrupt := range []string{"keys", "version", "symlink", "permissions", "current-rollback", "history-request"} {
		t.Run(corrupt, func(t *testing.T) {
			e, key, req := sshAccessFixture(t)
			ctx := context.Background()
			first, err := e.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 1))
			require.NoError(t, err)
			req.ID, req.ExpectedRevision = "rotate", 1
			req.Config.AuthorizedKeys = []string{sshAccessKey(t)}
			_, err = e.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 2))
			require.NoError(t, err)
			store := e.sshAccessStore()
			path := filepath.Join(store.Root, sshAccessName(req.Config.User, req.Config.AccessID))
			var record sshAccessRecord
			require.NoError(t, readJSON(path, &record))
			switch corrupt {
			case "keys":
				receipt := record.current()
				receipt.Request.Config.AuthorizedKeys = []string{sshAccessKey(t)}
				record.History[record.CurrentID] = receipt
			case "version":
				record.Version = 0
			case "current-rollback":
				record.CurrentID = "create"
			case "history-request":
				receipt := record.History["create"]
				receipt.Request.Config.User = "other"
				record.History["create"] = receipt
			}
			require.NoError(t, atomicJSON(path, record))
			if corrupt == "symlink" {
				require.NoError(t, os.Rename(path, path+".original"))
				require.NoError(t, os.Symlink(path+".original", path))
			} else if corrupt == "permissions" {
				require.NoError(t, os.Chmod(path, 0666))
			}
			keys, err := e.SSHAuthorizedKeys(ctx, "ubuntu", "127.0.0.1", 22)
			require.Error(t, err)
			require.Empty(t, keys)
			_, err = e.ObserveSSHAccess(ctx, first.Handle)
			require.Error(t, err)
			remove := substrate.SSHAccessMutation{ID: "delete", ExpectedRevision: 2, Delete: true,
				Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
			_, err = e.MutateSSHAccess(ctx, remove, sshAccessGrant(t, key, remove, 3))
			require.Error(t, err)
			_, err = os.Lstat(path)
			require.NoError(t, err)
		})
	}
}

func TestSSHAccessGrantAndOwnerBoundaries(t *testing.T) {
	for _, changed := range []string{"expired", "boot", "scope", "generation", "session", "operation", "payload"} {
		t.Run(changed, func(t *testing.T) {
			e, key, req := sshAccessFixture(t)
			ctx := context.Background()
			_, err := e.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 1))
			require.NoError(t, err)
			original := req.Config.AuthorizedKeys[0]
			req.ID, req.ExpectedRevision = "rotate", 1
			req.Config.AuthorizedKeys = []string{sshAccessKey(t)}
			grant := sshAccessGrant(t, key, req, 1)
			switch changed {
			case "expired":
				grant.Grant.ExpiresAt = time.Now().Add(-time.Minute)
			case "boot":
				grant.Grant.TargetBootID = "other"
				grant.BootID = "other"
			case "scope":
				grant.Grant.Scope.TopologyUID = "other"
			case "generation":
				grant.Grant.Scope.Generation++
			case "session":
				grant.Grant.SessionID = "other"
			case "operation":
				grant.Grant.Operation = "guest_task"
			}
			grant, err = controlplane.SignExecutionGrant(key, grant.Grant, grant.BootID, time.Now().Add(-2*time.Minute))
			require.NoError(t, err)
			if changed == "payload" {
				req.Config.AccessID = "other"
			}
			_, err = e.MutateSSHAccess(ctx, req, grant)
			require.Error(t, err)
			keys, err := e.SSHAuthorizedKeys(ctx, "ubuntu", "127.0.0.1", 22)
			require.NoError(t, err)
			require.Equal(t, original+"\n", string(keys))
		})
	}
}

func TestSSHAccessReadDoesNotInitializeStore(t *testing.T) {
	e, _, _ := sshAccessFixture(t)
	keys, err := e.SSHAuthorizedKeys(context.Background(), "ubuntu", "127.0.0.1", 22)
	require.NoError(t, err)
	require.Empty(t, keys)
	entries, err := os.ReadDir(e.Root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSSHAccessColdRetryAfterEpochPublicationBeforeKeyCommit(t *testing.T) {
	e, key, req := sshAccessFixture(t)
	ctx := context.Background()
	grant := sshAccessGrant(t, key, req, 2)
	// Persist exactly the fence that precedes key publication, then cold-open.
	// No key record exists: a lower epoch is fenced, the original epoch retries.
	epochPath, epoch, unlock, err := e.lockScope(ctx, grant.Grant)
	require.NoError(t, err)
	epoch.Epoch, epoch.Session = grant.Grant.Epoch, grant.Grant.SessionID
	require.NoError(t, atomicJSON(epochPath, epoch))
	unlock()
	cold := &Engine{Root: e.Root, PublicKey: e.PublicKey}
	_, err = cold.MutateSSHAccess(ctx, req, sshAccessGrant(t, key, req, 1))
	require.ErrorIs(t, err, ErrUnauthorized)
	keys, err := cold.SSHAuthorizedKeys(ctx, "ubuntu", "127.0.0.1", 22)
	require.NoError(t, err)
	require.Empty(t, keys)
	result, err := cold.MutateSSHAccess(ctx, req, grant)
	require.NoError(t, err)
	require.Equal(t, uint64(1), result.Handle.Revision)
	retry, err := cold.MutateSSHAccess(ctx, req, grant)
	require.NoError(t, err)
	require.Equal(t, result, retry)
}
