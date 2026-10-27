package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreviousBootTaskDoesNotBlockCurrentScopeOrInventOutcome(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	e := &Engine{Root: t.TempDir(), PublicKey: public}
	require.NoError(t, os.Chmod(e.Root, 0700))
	boot, err := BootID()
	require.NoError(t, err)
	old := signedRequest(t, key, "old-boot", "old-task")
	r := Record{Version: RecordVersion, ID: old.ID, Digest: old.Grant.Grant.Digest, Grant: old.Grant, BootID: old.Grant.BootID, DeadlineBootNS: int64(time.Minute), Status: "running"}
	require.NoError(t, e.save(r))
	next := signedRequest(t, key, boot, "successor")
	g := next.Grant.Grant
	g.Epoch++
	require.NoError(t, e.scopeQuiescent(g))
	retained, err := e.load(old.ID)
	require.NoError(t, err)
	require.Equal(t, r, retained, "admission must not rewrite unknown history")
	_, err = e.Start(context.Background(), old)
	require.Error(t, err, "old signed request cannot execute on new boot")
	r.BootID = boot
	require.NoError(t, e.save(r))
	require.Error(t, e.scopeQuiescent(g), "mismatched boot/grant is not evidence")
	r.BootID = old.Grant.BootID
	r.Grant.Signature[0] ^= 1
	require.NoError(t, e.save(r))
	require.Error(t, e.scopeQuiescent(g), "forged historical grant is not evidence")
}
