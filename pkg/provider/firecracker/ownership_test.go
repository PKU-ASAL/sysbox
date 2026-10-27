package firecracker

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaleCleanupRejectsSocketWithoutOwnershipAnchor(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	hs := h.Provider.(*HandleState)
	require.NoError(t, os.Remove(hs.ManifestPath))
	require.ErrorContains(t, s.DestroyNode(context.Background(), h), "manifest missing")
	require.DirExists(t, hs.VMDir)
}

func TestStaleCleanupRemovesSocketForExitedOwnedProcess(t *testing.T) {
	s, h, _, cmd := manifestFixture(t, true)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	require.NoError(t, s.DestroyNode(context.Background(), h))
	require.NoFileExists(t, h.Provider.(*HandleState).Socket)
}
