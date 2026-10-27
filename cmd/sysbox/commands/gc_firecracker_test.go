package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/persistence"
	fc "github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGCPreviewRefusesCorruptFirecrackerInventory(t *testing.T) {
	root, _ := gcInventoryFixture(t)
	vmRoot := t.TempDir()
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_WORKDIR", vmRoot)
	gcFakeVirsh(t, "exit 0\n")
	gcFakeDocker(t, nil, nil)
	require.NoError(t, os.WriteFile(filepath.Join(vmRoot, "manifest.json"), []byte(`{`), 0600))
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	found, err := findOrphans(context.Background(), active)
	require.ErrorContains(t, err, "invalid inventory manifest")
	require.Nil(t, found)
}

type gcVMInventory struct {
	handles []substrate.NodeHandle
	err     error
}

func (i gcVMInventory) ListManagedNodes(context.Context) ([]substrate.NodeHandle, error) {
	return i.handles, i.err
}

func TestGCFirecrackerCandidatesRequireCompleteOwnedUnreferencedInventory(t *testing.T) {
	active := activeIDs{owners: map[string]bool{"owner": true}, inventories: map[string]string{"owner": "references"}, firecracker: map[string]bool{"retained": true}}
	handle := func(id, owner string) substrate.NodeHandle {
		return substrate.NodeHandle{ID: id, Generation: 2, Provider: &fc.HandleState{OwnerUID: owner, TopologyUID: owner, ResourceUID: "resource", Generation: 2}}
	}
	i := gcVMInventory{handles: []substrate.NodeHandle{handle("retained", "owner"), handle("orphan", "owner"), handle("foreign", "other")}}
	found, err := findOrphanFirecracker(context.Background(), active, i, io.Discard)
	require.NoError(t, err)
	require.Equal(t, []orphan{{Kind: "firecracker", ID: "orphan", Topo: "owner", OwnerUID: "owner", ResourceUID: "resource", Generation: 2, InventoryDigest: "references"}}, found)
	i.err = errors.New("manifest changed")
	found, err = findOrphanFirecracker(context.Background(), active, i, io.Discard)
	require.ErrorContains(t, err, "manifest changed")
	require.Nil(t, found, "partial results cannot produce GC candidates")
	i.err = nil
	i.handles = append(i.handles, substrate.NodeHandle{ID: "invalid", Generation: 2})
	found, err = findOrphanFirecracker(context.Background(), active, i, io.Discard)
	require.Error(t, err)
	require.Nil(t, found)
}

func TestGCProtectsFirecrackerStateAndSnapshotReferences(t *testing.T) {
	root := t.TempDir()
	repo, err := persistence.OpenFilesystem(filepath.Join(root, "lab", "state.json.lifecycle"))
	require.NoError(t, err)
	defer repo.Close()
	manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
	st, err := manager.Load()
	require.NoError(t, err)
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "vm"), Driver: "firecracker", ExternalID: "vm-id", OwnerUID: st.TopologyUID, ResourceUID: "vm-resource", Generation: 1}}
	require.NoError(t, manager.Save(st))
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	require.True(t, active.firecracker["vm-id"])
	snapshot, err := manager.Snapshot(context.Background(), "retain VM")
	require.NoError(t, err)
	st.Resources = nil
	require.NoError(t, manager.Save(st))
	active, err = collectActiveIDsChecked(root)
	require.NoError(t, err)
	require.True(t, active.firecracker["vm-id"], "snapshot-only VM must remain protected")
	require.NoError(t, manager.Backend().(state.SnapshotDeleter).DeleteSnapshot(context.Background(), snapshot.ID))
	active, err = collectActiveIDsChecked(root)
	require.NoError(t, err)
	require.False(t, active.firecracker["vm-id"])
}
