package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func quiescenceFixture(t *testing.T) (*Substrate, substrate.NodeHandle, substrate.OperationIdentity, func()) {
	t.Helper()
	root := t.TempDir()
	id := substrate.OperationIdentity{ActionID: "action-1", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	vmID := "fc-quiescence"
	dir := filepath.Join(root, id.TopologyUID, id.ResourceUID, "1")
	require.NoError(t, os.MkdirAll(dir, 0700))
	cfg := filepath.Join(dir, "vm_config.json")
	require.NoError(t, os.WriteFile(cfg, []byte(`{}`), 0600))
	socket := ownedUnixSocketPath(dir, vmID, "firecracker.sock", "fc")
	require.NoError(t, ensureOwnedSocketDirectory(socket))
	hs := &HandleState{Version: manifestVersion, VMDir: dir, ConfigPath: cfg, Socket: socket}
	m := newManifest(substrate.WithOperationIdentity(context.Background(), id), root, vmID, dir, hs, "")
	require.NoError(t, refreshConfigDigest(m))
	require.NoError(t, saveManifest(m))
	s := &Substrate{rootfsDir: root}
	h := manifestHandle(m)
	cleanup := func() { _ = os.RemoveAll(root) }
	return s, h, id, cleanup
}

func TestFirecrackerProveQuiescenceUsesDurableVMOperationAnchor(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()

	proof, err := s.ProveQuiescence(context.Background(), h, "op-1", id)
	require.NoError(t, err)
	require.Equal(t, "op-1", proof.OperationID)
	require.Equal(t, id, proof.Identity)
	require.NoError(t, proof.Validate(id, "op-1"))
}

func TestFirecrackerProveQuiescenceWaitsForHostOperationLock(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	lockRelease, err := s.lockHostOperation(context.Background(), h)
	require.NoError(t, err)
	defer lockRelease()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = s.ProveQuiescence(ctx, h, "op-1", id)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestFirecrackerProveQuiescenceRejectsWrongGeneration(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	id.Generation++
	_, err := s.ProveQuiescence(context.Background(), h, "op-1", id)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity")
}
