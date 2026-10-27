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

func TestAdoptionInspectionNeverRepairsLaunchManifest(t *testing.T) {
	s, h, _, cleanup := quiescenceFixture(t)
	defer cleanup()
	m, err := s.loadManifest(context.Background(), h)
	require.NoError(t, err)
	m.Phase = "starting"
	m.Process.PID = 0
	require.NoError(t, saveManifest(m))
	before, err := os.ReadFile(m.Handle.ManifestPath)
	require.NoError(t, err)
	_, err = s.InspectAdoption(context.Background(), h.ID, substrate.NodeSpec{})
	require.ErrorContains(t, err, "read-only inspection")
	after, err := os.ReadFile(m.Handle.ManifestPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
