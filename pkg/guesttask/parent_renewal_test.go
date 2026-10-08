package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRenewCannotReparentExistingGuestTask(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	boot, err := BootID()
	require.NoError(t, err)
	request := signedRequest(t, private, boot, "bound-child")
	request.Grant.Grant.ParentEpoch = 8
	request.Grant, err = controlplane.SignExecutionGrant(private, request.Grant.Grant, boot, time.Now())
	require.NoError(t, err)
	engine := &Engine{Root: t.TempDir(), PublicKey: public}
	require.NoError(t, os.Chmod(engine.Root, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(engine.Root, request.ID), 0700))
	now, err := bootNanos()
	require.NoError(t, err)
	record := Record{Version: Version, ID: request.ID, Digest: request.Grant.Grant.Digest, Grant: request.Grant, BootID: boot, DeadlineBootNS: now + int64(time.Minute), Status: "running"}
	require.NoError(t, engine.save(record))
	for _, parent := range []uint64{0, 9} {
		next := request.Grant.Grant
		next.ParentEpoch = parent
		next.ExpiresAt = time.Now().Add(2 * time.Minute)
		signed, err := controlplane.SignExecutionGrant(private, next, boot, time.Now())
		require.NoError(t, err)
		_, err = engine.Renew(context.Background(), request.ID, record.Digest, signed)
		require.ErrorIs(t, err, ErrUnauthorized)
	}
	stored, err := engine.load(request.ID)
	require.NoError(t, err)
	require.Equal(t, request.Grant, stored.Grant)
	require.Equal(t, record.DeadlineBootNS, stored.DeadlineBootNS)
}
