//go:build e2e

package libvirt

import (
	"context"
	"encoding/xml"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLibvirtRealGCExactOwnerAndSurvivorE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("real libvirt explicitly required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sub := New(t.TempDir())
	owner := uuid.NewString()
	create := func() (string, string, string) {
		id := managedOperation()
		id.OwnerUID = owner
		id.TopologyUID = owner
		id.ResourceUID = uuid.NewString()
		id.Generation = 1
		domainID := uuid.NewString()
		dir := t.TempDir()
		disk := filepath.Join(dir, "keep.qcow2")
		require.NoError(t, os.WriteFile(disk, []byte("shared disk sentinel"), 0600))
		hs := &HandleState{DomainName: "gc-" + domainID, DomainUUID: domainID, Identity: &id, VMDir: dir, DiskPath: disk}
		raw, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: domainID, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &id, ManagedState: hs})
		require.NoError(t, err)
		var definition domainXML
		require.NoError(t, xml.Unmarshal([]byte(raw), &definition))
		definition.Devices.Disks = nil
		encoded, err := xml.Marshal(definition)
		require.NoError(t, err)
		path := filepath.Join(dir, "domain.xml")
		require.NoError(t, os.WriteFile(path, encoded, 0600))
		out, err := exec.CommandContext(ctx, "virsh", "define", path).CombinedOutput()
		require.NoError(t, err, string(out))
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = exec.CommandContext(cleanup, "virsh", "destroy", domainID).CombinedOutput()
			_, _ = exec.CommandContext(cleanup, "virsh", "undefine", domainID).CombinedOutput()
		})
		out, err = exec.CommandContext(ctx, "virsh", "start", domainID).CombinedOutput()
		require.NoError(t, err, string(out))
		return domainID, id.ResourceUID, disk
	}
	target, uid, disk := create()
	survivor, _, survivorDisk := create()
	require.Error(t, sub.DeleteManagedExternal(ctx, target, owner, uid, 2))
	present, err := gcDomainPresent(ctx, target)
	require.NoError(t, err)
	require.True(t, present)
	require.NoError(t, sub.DeleteManagedExternal(ctx, target, owner, uid, 1))
	present, err = gcDomainPresent(ctx, target)
	require.NoError(t, err)
	require.False(t, present)
	present, err = gcDomainPresent(ctx, survivor)
	require.NoError(t, err)
	require.True(t, present)
	for _, path := range []string{disk, survivorDisk} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "shared disk sentinel", string(raw))
	}
	require.NoError(t, sub.DeleteManagedExternal(ctx, target, owner, uid, 1), "exact UUID absence is idempotent")
}
