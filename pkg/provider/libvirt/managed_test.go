package libvirt

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func managedOperation() substrate.OperationIdentity {
	return substrate.OperationIdentity{OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 2, ActionID: "create"}
}
func virshFixture(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "virsh"), body)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLibvirtDiscoveryRequiresSuccessfulBoundedAbsence(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "unavailable"}[unavailable], func(t *testing.T) {
			code := "0"
			if unavailable {
				code = "1"
			}
			virshFixture(t, "#!/bin/sh\nexit "+code+"\n")
			_, err := New(t.TempDir()).DiscoverManagedNode(context.Background(), managedOperation(), "node")
			expected := driver.ErrorNotFound
			if unavailable {
				expected = driver.ErrorUnavailable
			}
			require.True(t, driver.IsCategory(err, expected), "%v", err)
		})
	}
}

func TestLibvirtCreateRefusesSameNameForeignDomainWithoutMutation(t *testing.T) {
	log := filepath.Join(t.TempDir(), "mutations")
	t.Setenv("SYSBOX_MANAGED_MUTATIONS", log)
	virshFixture(t, `#!/bin/sh
case "$1" in
 list) printf "node\n";;
 dumpxml) printf '<domain><name>node</name><uuid>foreign</uuid></domain>\n';;
 domstate) printf "running\n";;
 dominfo) printf "Title: sysbox-managed\n";;
 *) printf "%s\n" "$*" >> "$SYSBOX_MANAGED_MUTATIONS";;
esac
`)
	_, err := New(t.TempDir()).CreateNode(substrate.WithOperationIdentity(context.Background(), managedOperation()), substrate.NodeSpec{Name: "node", Image: substrate.ArtifactHandle{ID: "base"}})
	require.Error(t, err)
	_, statErr := os.Stat(log)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestDomainXMLPersistsIdentityAndActualUUIDBeforeDefine(t *testing.T) {
	id := managedOperation()
	hs := &HandleState{DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", VMDir: "/owned", DiskPath: "/owned/disk.qcow2"}
	raw, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: hs.DomainUUID, DiskPath: hs.DiskPath, VCPUs: 1, MemoryMiB: 512, Identity: &id, ManagedState: hs})
	require.NoError(t, err)
	var parsed domainXML
	require.NoError(t, xml.Unmarshal([]byte(raw), &parsed))
	require.NotNil(t, parsed.Metadata)
	require.NotNil(t, parsed.Metadata.Managed)
	require.Equal(t, id.OwnerUID, parsed.Metadata.Managed.OwnerUID)
	require.Equal(t, id.ResourceUID, parsed.Metadata.Managed.ResourceUID)
	require.Equal(t, id.Generation, parsed.Metadata.Managed.Generation)
	require.Equal(t, hs.DomainUUID, parsed.UUID)
}

func TestLibvirtPreparedGenerationRecoversSameUUIDWithoutNewDisk(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "virsh"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "qemu-img"), `#!/bin/sh
case "$1" in
 convert) touch "$7";;
 create) touch "$8";;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	sub := New(t.TempDir())
	id := managedOperation()
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	spec := substrate.NodeSpec{Name: "node", Image: substrate.ArtifactHandle{ID: "base"}, ProviderConfig: &Config{VCPUs: 1, Memory: "512", SSHUser: "root"}}
	created, err := sub.CreateNode(ctx, spec)
	require.NoError(t, err)
	hs := hsFrom(created)
	before, err := os.Stat(hs.DiskPath)
	require.NoError(t, err)
	observe := id
	observe.ActionID = "observe"
	restored, err := New(sub.vmDirRoot()).DiscoverManagedNode(context.Background(), observe, "node")
	require.NoError(t, err)
	require.Equal(t, created.ID, restored.ID)
	require.EqualValues(t, 2, restored.Generation)
	repeated, err := sub.CreateNode(ctx, spec)
	require.NoError(t, err)
	require.Equal(t, created.ID, repeated.ID)
	after, err := os.Stat(hs.DiskPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
}

func TestLibvirtStartRejectsSameUUIDWithForeignOwner(t *testing.T) {
	virshFixture(t, `#!/bin/sh
case "$1" in
 list) printf "node\n";;
 dumpxml) printf '<domain><name>node</name><uuid>11111111-2222-4333-8444-555555555555</uuid></domain>\n';;
 domuuid) printf "11111111-2222-4333-8444-555555555555\n";;
 domstate) printf "running\n";;
esac
`)
	id := managedOperation()
	hs := &HandleState{DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &id}
	err := New(t.TempDir()).StartNode(context.Background(), managedNodeHandle(hs))
	require.Error(t, err)
}

func TestLibvirtDestroyDoesNotTargetReplacementByName(t *testing.T) {
	log := filepath.Join(t.TempDir(), "mutations")
	t.Setenv("SYSBOX_MANAGED_MUTATIONS", log)
	virshFixture(t, `#!/bin/sh
case "$1" in
 list) printf "99999999-2222-4333-8444-555555555555\n";;
 destroy|undefine) printf "%s\n" "$*" >> "$SYSBOX_MANAGED_MUTATIONS";;
esac
`)
	hs := &HandleState{DomainName: "reused-name", DomainUUID: "11111111-2222-4333-8444-555555555555"}
	require.NoError(t, New(t.TempDir()).DestroyNode(context.Background(), managedNodeHandle(hs)))
	_, err := os.Stat(log)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLibvirtDiscoveryRejectsSymlinkedGeneration(t *testing.T) {
	virshFixture(t, "#!/bin/sh\nexit 0\n")
	sub := New(t.TempDir())
	id := managedOperation()
	dir, err := sub.managedVMDir(id, "node")
	require.NoError(t, err)
	target := t.TempDir()
	require.NoError(t, os.Symlink(target, dir))
	_, err = sub.DiscoverManagedNode(context.Background(), id, "node")
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "symlink must be an ownership conflict: %v", err)
}
