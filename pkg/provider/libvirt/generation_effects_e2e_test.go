//go:build e2e

package libvirt

import (
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// A diskless VM exercises real libvirt identity/state without guest images or
// access to existing domains. No SSH success or reset acceptance is implied.
func TestLibvirtRealGenerationEffectsE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("set SYSBOX_LIBVIRT_E2E=1 and LIBVIRT_DEFAULT_URI for real libvirt acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := managedOperation()
	id.OwnerUID = uuid.NewString()
	id.ResourceUID = uuid.NewString()
	id.TopologyUID = uuid.NewString()
	sub := New(t.TempDir())
	name := "sysbox-fence-e2e-" + uuid.NewString()
	dir, err := sub.managedVMDir(id, name)
	require.NoError(t, err)
	hs := &HandleState{DomainName: name, DomainUUID: uuid.NewString(), Identity: &id, VMDir: dir, DiskPath: filepath.Join(dir, "disk.qcow2")}
	raw, err := GenerateDomainXML(DomainSpec{Name: name, UUID: hs.DomainUUID, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &id, ManagedState: hs})
	require.NoError(t, err)
	var domain domainXML
	require.NoError(t, xml.Unmarshal([]byte(raw), &domain))
	domain.Devices.Disks = nil
	definition, err := xml.Marshal(domain)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, definition, 0600))
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "virsh", args...).CombinedOutput()
		require.NoError(t, err, "%s: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	run("define", path)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Only the random UUID defined by this test is eligible for cleanup.
		_, _ = exec.CommandContext(cleanup, "virsh", "destroy", hs.DomainUUID).CombinedOutput()
		_, _ = exec.CommandContext(cleanup, "virsh", "undefine", hs.DomainUUID).CombinedOutput()
	})
	run("start", hs.DomainUUID)
	h := managedNodeHandle(hs)
	old := id
	old.Generation--
	stale := substrate.WithOperationIdentity(ctx, old)
	for _, effect := range []struct {
		name string
		call func(context.Context, substrate.NodeHandle) error
	}{{"start", sub.StartNode}, {"stop", sub.StopNode}, {"pause", sub.Pause}, {"resume", sub.Resume}, {"destroy", sub.DestroyNode}} {
		err := effect.call(stale, h)
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%s: %v", effect.name, err)
		require.Equal(t, "running", run("domstate", h.ID))
	}
	// Also reject a self-consistent old handle against actual newer metadata.
	oldState := *hs
	oldState.Identity = &old
	err = sub.Pause(stale, managedNodeHandle(&oldState))
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
	require.Equal(t, "running", run("domstate", h.ID))
	current := substrate.WithOperationIdentity(ctx, id)
	require.NoError(t, sub.Pause(current, h))
	require.Equal(t, "paused", run("domstate", h.ID))
	require.NoError(t, sub.Resume(current, h))
	require.Equal(t, "running", run("domstate", h.ID))
	require.NoError(t, sub.DestroyNode(current, h))
	out := run("list", "--all", "--uuid")
	require.NotContains(t, strings.Fields(out), h.ID)
	t.Log("real domain rejected stale context and stale handle; matching generation paused, resumed and destroyed")
}
