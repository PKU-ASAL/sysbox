package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type fenceLocalProvider interface {
	HostFenceScope(context.Context) (string, error)
	HostFenceDeletionAbsent(context.Context, substrate.NodeHandle) error
	HostFenceAttachmentAbsent(context.Context, substrate.NodeHandle, string, json.RawMessage) error
}

func TestFirecrackerHostFenceRequiresAllGenerationResidueAbsent(t *testing.T) {
	s := &Substrate{rootfsDir: filepath.Join(t.TempDir(), strings.Repeat("x", 60))}
	require.NoError(t, os.MkdirAll(s.rootfsDir, 0700))
	hs := &HandleState{TopologyUID: "owner", VMDir: filepath.Join(s.rootfsDir, "owner", "resource", "1")}
	hs.VsockUDS = ownedUnixSocketPath(hs.VMDir, "resource", "vsock.sock", "vs")
	require.NoError(t, ensureOwnedSocketDirectory(hs.VsockUDS))
	t.Cleanup(func() { _ = os.Remove(hs.VsockUDS) })
	seedOwnedManifest(t, s, "resource", hs, nil)
	h := firecrackerNodeHandle("resource", hs)
	proof, ok := any(s).(fenceLocalProvider)
	require.True(t, ok, "Firecracker must expose its exact local residue boundary")
	ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "dispose", OwnerUID: "fixture", TopologyUID: "owner", ResourceUID: "resource", Generation: 1})
	scope, err := proof.HostFenceScope(ctx)
	require.NoError(t, err)
	require.Equal(t, "local-only", scope)
	require.Error(t, proof.HostFenceDeletionAbsent(ctx, h), "existing stopped generation still has disk/config residue")
	require.NoError(t, s.DestroyNode(ctx, h))
	require.NoError(t, proof.HostFenceDeletionAbsent(ctx, h))
	require.NotEmpty(t, hs.VsockUDS)
	require.NoError(t, os.WriteFile(hs.VsockUDS, []byte("orphan endpoint"), 0600))
	require.Error(t, proof.HostFenceDeletionAbsent(ctx, h), "missing manifest must not hide a retained vsock endpoint")
	require.NoError(t, os.Remove(hs.VsockUDS))
	wrong := h
	copy := *hs
	copy.ResourceUID = "foreign"
	wrong.Provider = &copy
	require.Error(t, proof.HostFenceDeletionAbsent(ctx, wrong), "changed generation ownership cannot borrow the proof")
	require.Error(t, proof.HostFenceAttachmentAbsent(ctx, h, "eth0", json.RawMessage(`{}`)), "missing TAP identity is not absence")
}

func TestFirecrackerHostFenceCannotBorrowMissingDirectory(t *testing.T) {
	s := &Substrate{rootfsDir: t.TempDir()}
	hs := &HandleState{TopologyUID: "owner", VMDir: filepath.Join(s.rootfsDir, "owner", "resource", "1")}
	seedOwnedManifest(t, s, "resource", hs, nil)
	h := firecrackerNodeHandle("resource", hs)
	ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "dispose", OwnerUID: "fixture", TopologyUID: "owner", ResourceUID: "resource", Generation: 1})
	wrong := *hs
	wrong.VMDir = filepath.Join(s.rootfsDir, "unrelated-absent")
	wrong.Socket = ownedUnixSocketPath(wrong.VMDir, h.ID, "firecracker.sock", "fc")
	wrong.ConfigPath = filepath.Join(wrong.VMDir, "vm_config.json")
	wrong.ManifestPath = filepath.Join(wrong.VMDir, "manifest.json")
	fake := h
	fake.Provider = &wrong
	require.Error(t, s.HostFenceDeletionAbsent(ctx, fake), "the real owned generation remains, despite an absent supplied directory")
}
