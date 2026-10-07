package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAdoptionValidatesConfigBeyondManifestFingerprint(t *testing.T) {
	root := t.TempDir()
	s := &Substrate{rootfsDir: root}
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	dir := filepath.Join(root, "topology", "resource", "1")
	require.NoError(t, os.MkdirAll(dir, 0700))
	hs := &HandleState{VMDir: dir, Socket: ownedUnixSocketPath(dir, "node", "firecracker.sock", "fc"), ConfigPath: filepath.Join(dir, "vm_config.json")}
	require.NoError(t, os.WriteFile(hs.ConfigPath, []byte(`{}`), 0600))
	m := newManifest(ctx, root, "node", dir, hs, "")
	require.NoError(t, refreshConfigDigest(m))
	require.NoError(t, saveManifest(m))
	proof, err := s.InspectAdoption(ctx, "node", substrate.NodeSpec{})
	require.NoError(t, err)
	_, err = s.ObserveAdoption(ctx, proof)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hs.ConfigPath, []byte(`{"changed":true}`), 0600))
	_, err = s.ApplyAdoption(ctx, proof)
	require.Error(t, err, "unchanged manifest cannot attest a changed launch configuration")
	_, err = s.InspectAdoption(ctx, "node", substrate.NodeSpec{})
	require.Error(t, err)
}
